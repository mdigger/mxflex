package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/mdigger/csta"
	"github.com/mdigger/log"
	"github.com/mdigger/rest"
	"github.com/mdigger/sse"
)

var (
	appName = "mxflex"     // название сервиса
	version = "0.3"        // версия
	date    = "2017-08-13" // дата сборки
	git     = ""           // версия git
	build   = ""
)

func main() {
	var exts extList
	// exts.Set("3095,3099,3044")
	flag.Var(&exts, "ext", "comma-separated list of monitored `extensions`")
	var mxaddr = new(csta.Addr)
	// mxaddr, _ = csta.ParseURL("mx://d3test:981211@89.185.246.134")
	flag.Var(mxaddr, "mx", "mx url string in format `mx://login:password/host`")
	var host = "localhost:8080"
	flag.StringVar(&host, "host", host, "http server `host` name")
	var cstaOutput bool
	flag.BoolVar(&cstaOutput, "csta", cstaOutput, "CSTA output")
	var color bool
	flag.BoolVar(&color, "color", color, "color CSTA output")
	var logFlags int = log.Lindent
	flag.IntVar(&logFlags, "logflag", logFlags, "log flags")
	flag.Parse()

	log.SetFlags(logFlags)
	log.WithFields(log.Fields{
		"name":     appName,
		"version":  version,
		"build":    date,
		"revision": build,
		"git":      git,
	}).Info("starting service")
	log.SetLevel(log.DebugLevel)
	if cstaOutput {
		csta.SetLogOutput(os.Stdout)
		csta.SetLogFlags(0)
		if color {
			csta.LogTTY = true
		}
	}

	// инициализируем брокеров
	var brokers = make(map[string]*sse.Broker, exts.Len())
	for _, ext := range exts.List() {
		brokers[ext] = sse.New()
	}
	// инициализируем сервис
	var service = &Service{
		mxaddr:  mxaddr.Host,
		brokers: brokers,
	}
	// инициализируем обработку HTTP запросов
	var mux = &rest.ServeMux{
		Headers: map[string]string{
			"Server":            "MXCallMonitor/1.0",
			"X-API-Version":     "1.0",
			"X-Service-Version": version,
		},
		Logger: log.WithField("type", "http"),
	}
	mux.Handle("GET", "/", service.CallMonitor) // страница с мониторингом звонков
	mux.Handle("POST", "/", service.MakeCall)   // сделать звонок
	mux.Handle("GET", "/"+filepath.Base(htmlFile), rest.Redirect("/"))
	mux.Handle("GET", "/*file", rest.Files(filepath.Dir(htmlFile)))
	// инициализируем HTTP сервер
	server := &http.Server{
		Addr:         host,
		Handler:      mux,
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Minute * 5,
	}
	// добавляем автоматическую поддержку TLS сертификатов для сервиса
	if !strings.HasPrefix(host, "localhost") &&
		!strings.HasPrefix(host, "127.0.0.1") {
		manager := autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(host),
			Email:      "dmitrys@xyzrd.com",
			Cache:      autocert.DirCache("letsEncript.cache"),
		}
		server.TLSConfig = &tls.Config{
			GetCertificate: manager.GetCertificate,
		}
		server.Addr = ":https"
	}
	// запускаем HTTP сервер
	go func() {
		var secure = (server.Addr == ":https" || server.Addr == ":443")
		slog := log.WithFields(log.Fields{
			"address": server.Addr,
			"https":   secure,
		})
		if server.Addr != host {
			slog = slog.WithField("host", host)
		}
		slog.Info("starting http server")
		var err error
		if secure {
			err = server.ListenAndServeTLS("", "")
		} else {
			err = server.ListenAndServe()
		}
		if err != nil {
			log.WithError(err).Error("http server stoped")
			os.Exit(3)
		}
	}()

	// устанавливаем соединение с MX
	mxaddr.Type = "Server"
	log.WithFields(log.Fields{
		"host":  mxaddr.Host,
		"login": mxaddr.UserName,
		"type":  mxaddr.Type,
	}).Info("connecting to mx")
	mx, err := mxaddr.Client()
	if err != nil {
		log.WithError(err).Error("mx connection error")
		return
	}
	defer mx.Close()

	log.WithFields(log.Fields{
		"total": exts.Len(),
		"exts":  exts.String(),
	}).Info("starting user monitors")
	var monitors = mx.Monitor("DeliveredEvent")
	for _, ext := range exts.List() {
		// присоединяем к монитору данные о номере пользователя
		if err = monitors.Start(ext, ext); err != nil {
			break
		}
	}
	if err != nil {
		log.WithError(err).Error("monitor error")
		return
	}

	// начинаем обработку событий мониторинга
	for event := range monitors.Events {
		// проверяем, что данное событие относится к мониторингу
		ext, ok := event.Data.(string)
		if !ok {
			continue
		}
		// проверяем, что брокер для пользователя поддерживается
		broker, ok := brokers[ext]
		if !ok {
			continue
		}
		// преобразуем данные и отправляем брокеру
		switch event.Name {
		case "DeliveredEvent":
			var delivered = &MXDelivery{Timestamp: time.Now().Unix()}
			if err := event.Decode(delivered); err != nil {
				log.WithError(err).Error("bad delivery event")
				continue
			}
			data, err := json.Marshal(delivered)
			if err != nil {
				log.WithError(err).Error("bad delivery json format")
				continue
			}
			broker.Data("DeliveredEvent", string(data), "")
			log.WithFields(log.Fields{
				"callID": delivered.CallID,
				"from":   delivered.CallingDevice,
				"to":     delivered.CalledDevice,
				"ext":    ext,
			}).Info("incoming call")
		}
	}
}

// Delivery описывает структуру события входящего звонка
type MXDelivery struct {
	MonitorCrossRefID     uint64 `xml:"monitorCrossRefID" json:"-"`
	CallID                uint64 `xml:"connection>callID" json:"callId"`
	DeviceID              string `xml:"connection>deviceID" json:"deviceId"`
	GlobalCallID          string `xml:"connection>globalCallID" json:"globalCallId"`
	AlertingDevice        string `xml:"alertingDevice>deviceIdentifier" json:"alertingDevice"`
	CallingDevice         string `xml:"callingDevice>deviceIdentifier" json:"callingDevice"`
	CalledDevice          string `xml:"calledDevice>deviceIdentifier" json:"calledDevice"`
	LastRedirectionDevice string `xml:"lastRedirectionDevice>deviceIdentifier" json:"lastRedirectionDevice"`
	LocalConnectionInfo   string `xml:"localConnectionInfo" json:"localConnectionInfo"`
	Cause                 string `xml:"cause" json:"cause"`
	CallTypeFlags         uint32 `xml:"callTypeFlags" json:"callTypeFlags,omitempty"`
	Cads                  []struct {
		Name  string `xml:"name,attr" json:"name"`
		Type  string `xml:"type,attr" json:"type"`
		Value string `xml:",chardata" json:"value,omitempty"`
	} `xml:"cad,omitempty" json:"cads,omitempty"`
	Timestamp int64 `xml:"-" json:"time"`
}
