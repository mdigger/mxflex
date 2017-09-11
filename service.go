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

// Service описывает сервис по мониторингу звонков.
type Service struct {
	mxaddr  string
	cache   MXAuthCache
	brokers map[string]*sse.Broker
	phone   string
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
	// если информации нет в кеш, то необходимо авторизовать пользователя
	if ext == "" {
		// устанавливаем соединение с MX и проверяем логин и пароль пользователя
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
		return c.Error(http.StatusForbidden, "phone number is not monitored")
	}
	// разбираем заголовок запроса, чтобы понять что отдавать
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
	var params = new(struct {
		To string `json:"to" form:"to"`
	})
	if err := c.Bind(params); err != nil {
		return err
	}

	// устанавливаем соединение с MX и проверяем логин и пароль пользователя
	client, err := csta.NewClient(s.mxaddr, csta.Login{
		UserName: login,
		Password: password,
		Type:     "User",
		Platform: "CRM",
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

	// запускаем монитор
	_, err = client.SendWithResponse(&struct {
		XMLName xml.Name `xml:"MonitorStart"`
		Ext     string   `xml:"monitorObject>deviceObject"`
	}{
		Ext: client.Ext,
	}, csta.ReadTimeout)
	if err != nil {
		return err
	}
	// <iq vmdelay="30" ringdelay="1" mode="remote" type="set" id="mode"><address>+420720961083</address></iq>
	_, err = client.Send(&struct {
		XMLName   xml.Name `xml:"iq"`
		Type      string   `xml:"type,attr"`
		ID        string   `xml:"id,attr"`
		Mode      string   `xml:"mode,attr"`
		RingDelay uint16   `xml:"ringdelay,attr"`
		VMDelay   uint16   `xml:"vmdelay,attr"`
		From      string   `xml:"address"`
	}{
		Type:      "set",
		ID:        "mode",
		Mode:      "remote",
		RingDelay: 1,
		VMDelay:   30,
		From:      s.phone,
	})
	if err != nil {
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
		"mx":  client.SN,
		"ext": client.Ext,
		"to":  params.To,
	}).Debug("make call")
	return c.Write(result)
}
