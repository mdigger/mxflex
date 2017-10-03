package main

import (
	"encoding/base64"
	"flag"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mdigger/jwt"
	"github.com/mdigger/log"
)

// информация о сервисе и версия
var (
	appName = "MXFlex" // название сервиса
	version = "2.0"    // версия
	date    = ""       // дата сборки
	git     = ""       // версия git

	agent          = appName + "/" + version             // имя агента и сервера
	lowerAppName   = strings.ToLower(appName)            // используется как имя
	configName     = lowerAppName + ".json"              // конфигурационный файл
	adminTemplate  = lowerAppName + ".html"              // шаблон административного интерфейса
	adminHost      = ":12880"                            // адрес административного сервера
	logPath        = "/var/log/" + lowerAppName + ".log" // путь к файлам с логами
	manifestName   = "manifest.zip"
	srcManifestURL = "%host" // строку, которую надо заменить в манифесте на хост сервиса

	// jwtConfig описывает конфигурацию для создания токенов авторизации
	jwtConfig = &jwt.Config{
		Created: true,                // добавляем дату создания
		Expires: time.Hour,           // время жизни токена
		Key:     jwt.NewHS256Key(64), // ключ для подписи
	}
)

func init() {
	// log.SetFormat(log.Color{KeyIndent: 12})
	// выводим информацию о текущей версии
	var verInfoFields = []log.Field{
		log.Field{Name: "name", Value: appName},
		log.Field{Name: "version", Value: version},
	}
	if date != "" {
		verInfoFields = append(verInfoFields, log.Field{Name: "builded", Value: date})
	}
	if git != "" {
		verInfoFields = append(verInfoFields, log.Field{Name: "git", Value: git})
		agent += " (" + git + ")"
	}
	log.Info("service info", verInfoFields)

	flag.StringVar(&configName, "config", configName, "config `filename`")
	flag.StringVar(&adminHost, "admin", adminHost, "admin http server `host`")
	flag.StringVar(&adminTemplate, "template", adminTemplate, "admin template `filename`")
	flag.StringVar(&logPath, "log", logPath, "`path` to log files")
	flag.StringVar(&manifestName, "manifest", manifestName, "`path` to manifest file")
	flag.DurationVar(&jwtConfig.Expires, "token", jwtConfig.Expires, "jwt token `ttl`")
	flag.Parse()
}

func main() {
	config, err := LoadConfig(configName)
	if err != nil {
		log.Error("config error", err)
		os.Exit(2)
	}
	tmpl, err := template.ParseFiles(adminTemplate)
	if err != nil {
		log.Error("admin template error", err)
		os.Exit(2)
	}
	// выводим в лог ключ для подписи токенов
	log.Debug("jwt sign key", "key",
		base64.RawURLEncoding.EncodeToString(jwtConfig.Key.([]byte)))

	// запускаем прокси
	proxy, err := NewProxy(config)
	if err != nil {
		config.err = err
	}

	// запускаем административный веб сервер
	admin := &Admin{
		config: config,
		tmpl:   tmpl,
		proxy:  proxy,
		log:    log.New("admin"),
	}
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/", admin.Config)
	adminMux.HandleFunc("/manifest.zip", admin.Manifest)
	// отображаем либо каталог с логами, либо содержимое файла лога
	if fi, err := os.Stat(logPath); err != nil || fi.IsDir() {
		adminMux.Handle("/log/", http.StripPrefix(
			"/log/", http.FileServer(http.Dir(logPath))))
	} else {
		adminMux.HandleFunc("/log/", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, logPath)
		})
	}
	adminServer := http.Server{
		Addr:     adminHost,
		Handler:  admin.Authorization(adminMux), // проверяем авторизацию
		ErrorLog: admin.log.StdLog(log.ERROR),
	}
	admin.log.Info("service started", "addr", adminServer.Addr, "https", false)
	err = adminServer.ListenAndServe()
	adminServer.Close()
	admin.log.Info("service stopped", err)
}
