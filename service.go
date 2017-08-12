package main

import (
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
