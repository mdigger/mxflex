package main

import (
	"fmt"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mdigger/jwt"
	"github.com/mdigger/log"
	"github.com/mdigger/mx"
	"github.com/mdigger/rest"
	"github.com/mdigger/sse"
)

// HTTPHandler отвечает за обработку HTTP-запросов.
type HTTPHandler struct {
	mxServer *MXServer
	stopped  bool // флаг остановки сервиса
	mu       sync.RWMutex
}

// NewHTTPHandler инициализирует и возвращает обработчик HTTP-запросов к
// серверу MX.
func NewHTTPHandler(host, login, password string) (*HTTPHandler, error) {
	mxServer, err := NewMXServer(host, login, password)
	if err != nil {
		return nil, err
	}
	var handler = &HTTPHandler{mxServer: mxServer}
	// запускаем мониторинг разрыва соединения с сервером MX
	go func(mxs *MXServer) {
	wait:
		var err = <-mxs.conn.Done()
	reconnect:
		// прекращаем, если это остановка сервиса
		handler.mu.RLock()
		if handler.stopped {
			handler.mu.RUnlock()
			return
		}
		handler.mu.RUnlock()
		if err != nil {
			log.Error("mx connection error", err)
		}
		log.Info("reconnecting to mx", "delay", time.Minute.String())
		time.Sleep(time.Minute) // задержка перед переподключением
		mxs, err = NewMXServer(host, login, password)
		// подключаемся к серверу MX
		if err != nil {
			if _, ok := err.(*mx.LoginError); ok {
				log.Error("mx connection login error", err)
				return
			}
			goto reconnect
		}
		handler.mu.Lock()
		handler.mxServer = mxs
		handler.mu.Unlock()
		goto wait
	}(mxServer)
	return handler, nil
}

// Close закрывает соединение с сервером MX.
func (h *HTTPHandler) Close() error {
	h.mu.Lock()
	h.stopped = true
	var err = h.mxServer.Close()
	h.mu.Unlock()
	return err
}

// mx возвращает ссылку на MXServer, блокируя одновременный доступ на изменение.
func (h *HTTPHandler) mx() *MXServer {
	h.mu.RLock()
	var mxs = h.mxServer
	h.mu.RUnlock()
	return mxs
}

// Login авторизует пользователя MX, запускает мониторинг звонок для него и
// отдает токен для доступа к API.
func (h *HTTPHandler) Login(c *rest.Context) error {
	var (
		login    = c.Form("login")
		password = c.Form("password")
	)
	if login == "" {
		return c.Error(http.StatusBadRequest, "login required")
	}
	// авторизуем пользователя
	info, err := h.mx().Login(login, password)
	if err != nil {
		if errLogin, ok := err.(*mx.LoginError); ok {
			err = c.Error(http.StatusForbidden, errLogin.Error())
		} else if errNetwork, ok := err.(net.Error); ok && errNetwork.Timeout() {
			err = c.Error(http.StatusGatewayTimeout, errNetwork.Error())
		} else {
			err = c.Error(http.StatusServiceUnavailable, err.Error())
		}
		return err
	}
	// запускаем мониторинг звонков
	if err = h.mx().MonitorStart(info.Ext); err != nil {
		return err
	}
	// генерируем токен авторизации пользователя
	token, err := jwtConfig.Token(jwt.JSON{
		"sub": info.JID,
		"ext": info.Ext,
		"mx":  info.SN,
	})
	if err != nil {
		return err
	}
	return c.Write(&struct {
		Type    string  `json:"token_type,omitempty"`
		Token   string  `json:"access_token"`
		Expired float64 `json:"expires_in,omitempty"`
	}{
		Type:    "Bearer",
		Token:   token,
		Expired: jwtConfig.Expires.Seconds(),
	})
}

// tokenExt проверяет токен авторизации и возвращает внутренний номер
// пользователя MX.
func (h *HTTPHandler) tokenExt(c *rest.Context) (string, error) {
	var token = c.Request.FormValue("access_token")
	if token == "" {
		// запрашивает токен авторизации из заголовка
		var auth = c.Header("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			c.SetHeader("WWW-Authenticate",
				fmt.Sprintf("Bearer realm=%q", appName))
			return "", rest.ErrUnauthorized
		}
		token = strings.TrimPrefix(auth, "Bearer ")
	}
	if err := jwt.Verify(token, jwtConfig.Key); err != nil {
		return "", rest.NewError(http.StatusForbidden, err.Error())
	}
	var t = new(struct {
		Ext string `json:"ext"`
	})
	if err := jwt.Decode(token, t); err != nil {
		return "", rest.NewError(http.StatusForbidden, err.Error())
	}
	c.AddLogField("ext", t.Ext)
	return t.Ext, nil
}

// Logout останавливает мониторинг звонков пользователя.
func (h *HTTPHandler) Logout(c *rest.Context) error {
	ext, err := h.tokenExt(c) // распаковываем и проверяем токен
	if err != nil {
		return err
	}
	return h.mx().MonitorStop(ext) // останавливаем мониторинг
}

// MakeCall осуществляет серверный звонок.
func (h *HTTPHandler) MakeCall(c *rest.Context) error {
	ext, err := h.tokenExt(c) // распаковываем и проверяем токен
	if err != nil {
		return err
	}
	var (
		from = c.Form("from")
		to   = c.Form("to")
	)
	if from == "" {
		from = ext
	}
	if to == "" {
		return c.Error(http.StatusBadRequest, "to field is empty")
	}
	callInfo, err := h.mx().MakeCall(from, to)
	if err != nil {
		return err
	}
	return c.Write(rest.JSON{"call": callInfo})
}

// Events отдает события о звонках в виде SSE.
func (h *HTTPHandler) Events(c *rest.Context) error {
	ext, err := h.tokenExt(c) // распаковываем и проверяем токен
	if err != nil {
		return err
	}
	if mediatype, _, _ := mime.ParseMediaType(c.Header("Accept")); mediatype != sse.Mimetype {
		return c.Error(http.StatusNotAcceptable, "only sse support")
	}
	var broker *sse.Broker
	h.mx().monitors.Range(func(_, data interface{}) bool {
		var md = data.(*monitorData)
		if md.Extension == ext {
			broker = md.Broker
			return false
		}
		return true
	})
	if broker == nil {
		return c.Error(http.StatusForbidden, "not monitored")
	}
	var log = log.New("sse")
	log.Debug("connected", "count", broker.Connected()+1)
	// запускаем отдачу событий
	broker.ServeHTTP(c.Response, c.Request)
	log.Debug("disconnected", "count", broker.Connected())
	return nil
}

// ConnectionInfo отдает информацию об активных соединениях и мониторинге.
func (h *HTTPHandler) ConnectionInfo(c *rest.Context) error {
	return c.Write(rest.JSON{"monitoring": h.mx().ConnectionInfo()})
}

// Contacts отдает список контактов из серверной адресной книги.
func (h *HTTPHandler) Contacts(c *rest.Context) error {
	if _, err := h.tokenExt(c); err != nil {
		return err
	}
	return c.Write(rest.JSON{"contacts": h.mx().Contacts()})
}

// // CallHold подвешивает звонок.
// func (h *HTTPHandler) CallHold(c *rest.Context) error {
// 	if _, err := h.tokenExt(c); err != nil {
// 		return err
// 	}
// 	callID, err := strconv.ParseUint(c.Form("callId"), 10, 64)
// 	if err != nil {
// 		return c.Error(http.StatusBadRequest, "bad call id")
// 	}
// 	c.AddLogField("callId", callID)
// 	var deviceID = c.Form("deviceId")
// 	if deviceID == "" {
// 		return c.Error(http.StatusBadRequest, "device id required")
// 	}
// 	return h.mx().CallHold(callID, deviceID)
// }

// CallHangup сбрасывает звонок.
func (h *HTTPHandler) CallHangup(c *rest.Context) error {
	if _, err := h.tokenExt(c); err != nil {
		return err
	}
	callID, err := strconv.ParseUint(c.Form("callId"), 10, 64)
	if err != nil {
		return c.Error(http.StatusBadRequest, "bad call id")
	}
	c.AddLogField("callId", callID)
	var deviceID = c.Form("deviceId")
	if deviceID == "" {
		return c.Error(http.StatusBadRequest, "device id required")
	}
	return h.mx().CallHangup(callID, deviceID)
}

// CallTransfer перебрасывает звонок.
func (h *HTTPHandler) CallTransfer(c *rest.Context) error {
	if _, err := h.tokenExt(c); err != nil {
		return err
	}
	callID, err := strconv.ParseUint(c.Form("callId"), 10, 64)
	if err != nil {
		return c.Error(http.StatusBadRequest, "bad call id")
	}
	c.AddLogField("callId", callID)
	var deviceID = c.Form("deviceId")
	if deviceID == "" {
		return c.Error(http.StatusBadRequest, "device id required")
	}
	var destination = c.Form("destination")
	if destination == "" {
		return c.Error(http.StatusBadRequest, "destination phone required")
	}
	return h.mx().CallTransfer(callID, deviceID, destination)
}
