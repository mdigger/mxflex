package main

import (
	"encoding/xml"
	"fmt"
	"mime"
	"net"
	"net/http"

	"github.com/mdigger/csta"
	"github.com/mdigger/log"
	"github.com/mdigger/rest"
	"github.com/mdigger/sse"
)

type Service struct {
	mxaddr  string
	cache   MXAuthCache
	brokers map[string]*sse.Broker
}

var htmlFile = "./html/index.html"

// CallMonitor проверяет авторизацию пользователя и отдает страничку со
// скриптами или событиями, в зависимости от типа запроса.
func (s *Service) CallMonitor(c *rest.Context) error {
	// проверяем авторизацию пользователя
	login, password, ok := c.BasicAuth()
	if !ok {
		// TODO: адрес MX
		c.SetHeader("WWW-Authenticate", fmt.Sprintf("Basic realm=%s", appName))
		return rest.ErrUnauthorized
	}
	c.AddLogField("login", login)
	// запрашиваем кеш на получение внутреннего номера пользователя
	var ext = s.cache.Check(login, password)
	// если информации нет в кеше, то необходимо авторизовать пользователя
	if ext == "" {
		// устанавливаем соединие с MX и проверяем логин и пароль пользователя
		client, err := csta.NewClient(s.mxaddr, csta.Login{
			UserName: login,
			Password: password,
			Type:     "User",
			Platform: "iPhone",
			Version:  "1.0",
		})
		if err != nil {
			if errLogin, ok := err.(*csta.LoginError); ok {
				err = c.Error(http.StatusForbidden, errLogin.Error())
			} else if errNetwork, ok := err.(net.Error); ok && errNetwork.Timeout() {
				err = c.Error(http.StatusGatewayTimeout, errNetwork.Error())
			} else {
				err = c.Error(http.StatusServiceUnavailable, err.Error())
			}
			return err
		}
		ext = client.Ext
		client.Close()
		// добавляем информацию в кеш в случае успешной авторизации
		s.cache.Add(login, password, ext)
	}

	// запрашиваем брокера SSE для данного номера пользователя
	var broker = s.brokers[ext]
	if broker == nil {
		return rest.ErrForbidden
	}
	// разбираем заголовок запроса, чтобы понять что отдавть
	mediatype, _, _ := mime.ParseMediaType(c.Header("Accept"))
	if mediatype != sse.Mimetype {
		// отдаем файл с HTML
		return c.ServeFile(htmlFile)
	}

	c.AddLogField("type", "sse")
	ctxlog := log.WithFields(log.Fields{
		"ext":   ext,
		"type":  "sse",
		"login": login,
	})
	ctxlog.WithField("count", broker.Connected()+1).
		Debug("sse client connected")
	// запускаем отдачу событий
	broker.ServeHTTP(c.Response, c.Request)
	ctxlog.WithField("count", broker.Connected()).
		Debug("sse client disconnected")
	return nil
}

// MakeCall выполняет звонок через сервер MX.
func (s *Service) MakeCall(c *rest.Context) error {
	// проверяем авторизацию пользователя
	login, password, ok := c.BasicAuth()
	if !ok {
		// TODO: адрес MX
		c.SetHeader("WWW-Authenticate", fmt.Sprintf("Basic realm=%s", appName))
		return rest.ErrUnauthorized
	}
	c.AddLogField("login", login)

	// разбираем параметры из запроса
	type Params struct {
		RingDelay uint8  `xml:"ringdelay,attr" json:"ringDelay" form:"ringDelay"`
		VMDelay   uint8  `xml:"vmdelay,attr" json:"vmDelay" form:"vmDelay"`
		From      string `xml:"address" json:"from" form:"from"`
		To        string `xml:"-" json:"to" form:"to"`
	}
	// инициализируем параметры по умолчанию и разбираем запрос
	var params = &Params{
		RingDelay: 1,
		VMDelay:   30,
	}
	if err := c.Bind(params); err != nil {
		return err
	}

	// устанавливаем соединие с MX и проверяем логин и пароль пользователя
	client, err := csta.NewClient(s.mxaddr, csta.Login{
		UserName: login,
		Password: password,
		Type:     "User",
		Platform: "iPhone",
		Version:  "1.0",
	})
	if err != nil {
		if errLogin, ok := err.(*csta.LoginError); ok {
			err = c.Error(http.StatusForbidden, errLogin.Error())
		} else if errNetwork, ok := err.(net.Error); ok && errNetwork.Timeout() {
			err = c.Error(http.StatusGatewayTimeout, errNetwork.Error())
		} else {
			err = c.Error(http.StatusServiceUnavailable, err.Error())
		}
		return err
	}
	// добавляем информацию в кеш в случае успешной авторизации
	s.cache.Add(login, password, client.Ext)
	defer client.Close()

	// отправляем команду на установку номера исходящего звонка
	if _, err = client.Send(&struct {
		XMLName xml.Name `xml:"iq"`
		Type    string   `xml:"type,attr"`
		ID      string   `xml:"id,attr"`
		Mode    string   `xml:"mode,attr"`
		*Params
	}{
		Type:   "set",
		ID:     "mode",
		Mode:   "remote",
		Params: params,
	}); err != nil {
		return err
	}

	// инициируем звонок на номер
	type callingDevice struct {
		Type string `xml:"typeOfNumber,attr"`
		Ext  string `xml:",chardata"`
	}
	var cmd = &struct {
		XMLName       xml.Name      `xml:"MakeCall"`
		CallingDevice callingDevice `xml:"callingDevice"`
		To            string        `xml:"calledDirectoryNumber"`
	}{
		CallingDevice: callingDevice{
			Type: "deviceID",
			Ext:  client.Ext,
		},
		To: params.To,
	}
	resp, err := client.SendWithResponse(cmd, csta.ReadTimeout)
	if err != nil {
		return err
	}
	// разбираем ответ
	var result = new(struct {
		CallID       uint64 `xml:"callingDevice>callID" json:"callId"`
		DeviceID     string `xml:"callingDevice>deviceID" json:"deviceId"`
		CalledDevice string `xml:"calledDevice" json:"called"`
	})
	if err := resp.Decode(result); err != nil {
		return err
	}
	log.WithFields(log.Fields{
		"mx":   client.SN,
		"ext":  client.Ext,
		"from": params.From,
		"to":   params.To,
	}).Debug("make call")
	return c.Write(result)

}
