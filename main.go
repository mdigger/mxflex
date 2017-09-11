package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v2"

	"github.com/mdigger/csta"
	"github.com/mdigger/log"
	"github.com/mdigger/rest"
	"github.com/mdigger/sse"
	"golang.org/x/crypto/acme/autocert"
)

var (
	appName = "mxflex"     // название сервиса
	version = "0.7"        // версия
	date    = "2017-09-11" // дата сборки
	git     = ""           // версия git

	phone string
)

func main() {
	var configName = appName + ".json"
	flag.StringVar(&configName, "config", configName, "config `filename`")
	var cstaOutput bool
	flag.BoolVar(&cstaOutput, "csta", cstaOutput, "CSTA output")
	var logFlags = log.LstdFlags //| log.Lindent
	flag.IntVar(&logFlags, "logflag", logFlags, "log flags")
	flag.Parse()

	log.SetFlags(logFlags)
	log.WithFields(log.Fields{
		"name":    appName,
		"version": version,
		"build":   date,
		"git":     git,
	}).Info("starting service")
	log.SetLevel(log.DebugLevel)
	if cstaOutput {
		csta.SetLogOutput(os.Stdout)
		csta.SetLogFlags(0)
	}

	var config = new(struct {
		Host string `json:"host"`
		MX   struct {
			Addr     string `json:"addr"`
			Login    string `json:"login"`
			Password string `json:"password"`
		} `json:"mx"`
		Phone string   `json:"phone"`
		Exts  []string `json:"exts"`
	})
	data, err := ioutil.ReadFile(configName)
	if err != nil {
		log.WithError(err).Error("config file error")
		os.Exit(2)
	}
	if err = yaml.Unmarshal(data, config); err != nil {
		log.WithError(err).Error("config error")
		os.Exit(2)
	}

	if config.Host == "" {
		config.Host = "localhost:8000"
	}
	// инициализируем брокеров
	if len(config.Exts) == 0 {
		log.Error("no monitoring exts")
		os.Exit(2)
	}
	if config.Phone == "" {
		log.Error("no outgoing phone number")
		os.Exit(2)
	}
	if config.MX.Addr == "" {
		log.Error("no mx address")
		os.Exit(2)
	}
	var brokers = make(map[string]*sse.Broker, len(config.Exts))
	for _, ext := range config.Exts {
		brokers[ext] = sse.New()
	}
	// инициализируем сервис
	var service = &Service{
		mxaddr:  config.MX.Addr,
		brokers: brokers,
		phone:   config.Phone,
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
	var server = &http.Server{
		Addr:         config.Host,
		Handler:      mux,
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Minute * 5,
	}
	// анализируем порт
	httphost, port, err := net.SplitHostPort(config.Host)
	if err, ok := err.(*net.AddrError); ok && err.Err == "missing port in address" {
		httphost = err.Addr
	}
	var isIP = (net.ParseIP(httphost) != nil)
	var notLocal = (httphost != "localhost" &&
		!strings.HasSuffix(httphost, ".local") &&
		!isIP)
	var canCert = notLocal && httphost != "" &&
		(port == "443" || port == "https" || port == "")

	// добавляем автоматическую поддержку TLS сертификатов для сервиса
	if canCert {
		manager := autocert.Manager{
			Prompt: autocert.AcceptTOS,
			HostPolicy: func(_ context.Context, host string) error {
				if config.Host != httphost {
					log.WithField("host", config.Host).Error("unsupported https host")
					return errors.New("acme/autocert: host not configured")
				}
				return nil
			},
			Email: "dmitrys@xyzrd.com",
			Cache: autocert.DirCache("letsEncrypt.cache"),
		}
		server.TLSConfig = &tls.Config{
			GetCertificate: manager.GetCertificate,
		}
		server.Addr = ":https"
	} else if port == "" {
		server.Addr = net.JoinHostPort(httphost, "http")
	} else {
		server.Addr = net.JoinHostPort(httphost, port)
	}
	// запускаем HTTP сервер
	go func() {
		log.WithFields(log.Fields{
			"address": server.Addr,
			"tls":     canCert,
			"host":    httphost,
		}).Info("starting http server")
		var err error
		if canCert {
			err = server.ListenAndServeTLS("", "")
		} else {
			err = server.ListenAndServe()
		}
		if err != nil {
			log.WithError(err).Error("http server stopped")
			os.Exit(2)
		}
	}()

	// устанавливаем соединение с MX
	log.WithFields(log.Fields{
		"host":  config.MX.Addr,
		"login": config.MX.Login,
		"type":  "Server",
	}).Info("connecting to mx")
	mx, err := csta.NewClient(config.MX.Addr, csta.Login{
		UserName: config.MX.Login,
		Password: config.MX.Password,
		Type:     "Server",
		Platform: "iPhone",
		Version:  "1.0",
	})
	if err != nil {
		log.WithError(err).Error("mx connection error")
		return
	}
	defer mx.Close()

	log.WithFields(log.Fields{
		"total": len(config.Exts),
		"exts":  strings.Join(config.Exts, ","),
	}).Info("starting user monitors")
	var monitors = mx.Monitor("DeliveredEvent")
	for _, ext := range config.Exts {
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
			log.Debug("no ext")
			continue
		}
		// проверяем, что брокер для пользователя поддерживается
		broker, ok := brokers[ext]
		if !ok {
			log.Debug("not monitored")
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
			// игнорируем, если не указано вызываемое устройство
			if delivered.CalledDevice == "" {
				log.Warning("ignore empty delivery")
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

// MXDelivery описывает структуру события входящего звонка
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
