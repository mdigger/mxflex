package main

import (
	"fmt"
	"mime"
	"net"
	"net/http"
	"strings"

	"github.com/mdigger/jwt"
	"github.com/mdigger/log"
	"github.com/mdigger/mx"
	"github.com/mdigger/rest"
	"github.com/mdigger/sse"
)

// Handler отвечает за обработку HTTP-запросов.
type Handler struct {
	monitor *MXMonitor
}

// Login авторизует пользователя MX, запускает мониторинг звонок для него и
// отдает токен для доступа к API.
func (h *Handler) Login(c *rest.Context) error {
	var (
		login    = c.Form("login")
		password = c.Form("password")
	)
	if login == "" {
		return c.Error(http.StatusBadRequest, "login required")
	}
	// авторизуем пользователя
	info, err := h.monitor.login(login, password)
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
	if err = h.monitor.monitorStart(info.Ext); err != nil {
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
func (h *Handler) tokenExt(c *rest.Context) (string, error) {
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
func (h *Handler) Logout(c *rest.Context) error {
	ext, err := h.tokenExt(c) // распаковываем и проверяем токен
	if err != nil {
		return err
	}
	return h.monitor.monitorStop(ext) // останавливаем мониторинг
}

// MakeCall осуществляет серверный звонок.
func (h *Handler) MakeCall(c *rest.Context) error {
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
	callInfo, err := h.monitor.makeCall(from, to)
	if err != nil {
		return err
	}
	return c.Write(rest.JSON{"call": callInfo})
}

// Events отдает события о звонках в виде SSE.
func (h *Handler) Events(c *rest.Context) error {
	ext, err := h.tokenExt(c) // распаковываем и проверяем токен
	if err != nil {
		return err
	}
	if mediatype, _, _ := mime.ParseMediaType(c.Header("Accept")); mediatype != sse.Mimetype {
		return c.Error(http.StatusNotAcceptable, "only sse support")
	}
	var broker *sse.Broker
	h.monitor.monitors.Range(func(_, data interface{}) bool {
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
	var ctxlog = log.WithFields(log.Fields{
		"ext":  ext,
		"type": "sse",
	})
	ctxlog.WithField("count", broker.Connected()+1).
		Debug("sse client connected")
	// запускаем отдачу событий
	broker.ServeHTTP(c.Response, c.Request)
	ctxlog.WithField("count", broker.Connected()).
		Debug("sse client disconnected")
	return nil
}

// ConnectionInfo отдает информацию об активных соединениях и мониторинге.
func (h *Handler) ConnectionInfo(c *rest.Context) error {
	return c.Write(rest.JSON{"monitoring": h.monitor.connectionInfo()})
}

// Contacts отдает список контактов из серверной адресной книги.
func (h *Handler) Contacts(c *rest.Context) error {
	if _, err := h.tokenExt(c); err != nil {
		return err
	}
	return c.Write(rest.JSON{"contacts": h.monitor.contacts()})
}
