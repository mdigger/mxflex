package main

import (
	"sync"
	"time"

	"github.com/mdigger/log"
)

// AuthCacheDuration содержит продолжительность, в течение которого
// считается, что пароль пользователя валидный и не требуется повторная проверка.
var AuthCacheDuration = time.Minute * 30

// cacheItem описывает кешируемую информацию об авторизации пользователя.
type cacheItem struct {
	Password string    // пароль пользователя
	Ext      string    // внутренний номер телефона
	Updated  time.Time // время внесения в кеш
}

// MXAuthCache содержит кеш с информацией об авторизации пользователей на
// сервера MX. Используется чтобы избежать повторной авторизации пользователя
// только для проверки верности логина и пароля.
type MXAuthCache struct {
	list map[string]cacheItem // список пользователей и их паролей
	mu   sync.RWMutex
}

// Check возвращает внутренний номер телефона, если авторизация не требуется.
func (a *MXAuthCache) Check(login, password string) string {
	a.mu.RLock()
	p, ok := a.list[login]
	a.mu.RUnlock()
	if !ok || (time.Since(p.Updated) > AuthCacheDuration) {
		return ""
	}
	log.WithFields(log.Fields{
		"login":   login,
		"inCache": ok,
	}).Debug("user login in cache")
	return p.Ext
}

// Add добавляет информацию об авторизации пользователя и его номере телефона
// в кеш.
func (a *MXAuthCache) Add(login, password string, ext string) {
	a.mu.Lock()
	if a.list == nil {
		a.list = make(map[string]cacheItem)
	}
	if _, ok := a.list[login]; !ok {
		log.WithField("login", login).Debug("add user login to cache")
	}
	a.list[login] = cacheItem{
		Ext:      ext,
		Password: password,
		Updated:  time.Now(),
	}
	a.mu.Unlock()
}
