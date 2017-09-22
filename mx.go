package main

import (
	"encoding/json"
	"encoding/xml"
	"sort"
	"sync"
	"time"

	"github.com/mdigger/jwt"
	"github.com/mdigger/log"
	"github.com/mdigger/mx"
	"github.com/mdigger/sse"
)

// jwtConfig описывает конфигурацию для создания токенов авторизации
var jwtConfig = &jwt.Config{
	Created: true,                // добавляем дату создания
	Expires: time.Hour,           // время жизни токена
	Key:     jwt.NewHS256Key(64), // ключ для подписи
}

// MXServer позволяет отслеживать информацию о звонках на сервер MX.
type MXServer struct {
	mxHost   string   // адрес сервера
	conn     *mx.Conn // серверное соединение с MX
	monitors sync.Map // идентификаторы запущенных мониторов и внутренние номера пользователей
	ab       sync.Map // серверная адресная книга
}

// NewMXServer подключается и возвращает серверное соединение с MX для
// мониторинга звонков.
func NewMXServer(mxHost, login, password string) (*MXServer, error) {
	conn, err := mx.Connect(mxHost)
	if err != nil {
		return nil, err
	}
	conn.SetLogger(log.New("MX Server"))
	if _, err = conn.Login(mx.Login{
		UserName: login,
		Password: password,
		Type:     "Server",
		Platform: "iPhone",
		Version:  "1.0",
	}); err != nil {
		conn.Close()
		return nil, err
	}
	var monitor = &MXServer{
		mxHost: mxHost,
		conn:   conn,
	}
	contacts, err := conn.Addressbook()
	if err != nil {
		conn.Close()
		return nil, err
	}
	for _, contact := range contacts {
		monitor.ab.Store(contact.JID, contact)
	}
	go monitor.monitoring() // запускаем мониторинг звонков
	return monitor, nil
}

// Close закрывает серверное соединение.
func (m *MXServer) Close() error {
	m.monitors.Range(func(mID, data interface{}) bool {
		m.monitors.Delete(mID)
		data.(*monitorData).Close()
		return false
	})
	return m.conn.Close()
}

// Login авторизует пользователя MX и возвращает информацию о нем.
func (m *MXServer) Login(login, password string) (*mx.Info, error) {
	log.Info("check mx login", "login", login)
	conn, err := mx.Connect(m.mxHost)
	if err != nil {
		return nil, err
	}
	conn.SetLogger(log.New("MX Login: " + login))
	loginInfo, err := conn.Login(mx.Login{
		UserName: login,
		Password: password,
		Type:     "User",
		Platform: "CRM",
		Version:  "1.0",
	})
	conn.Logout()
	conn.Close()
	if err != nil {
		return nil, err
	}
	return loginInfo, nil
}

// monitorData описывает ассоциированные с монитором данные.
type monitorData struct {
	Extension   string // внутренний номер пользователя
	*sse.Broker        // SSE-брокер для мониторинга событий
}

// MonitorStart запускает пользовательский монитор.
func (m *MXServer) MonitorStart(ext string) error {
	// проверяем, что монитор еще не запущен
	var started bool
	m.monitors.Range(func(_, data interface{}) bool {
		started = data.(*monitorData).Extension == ext
		return !started
	})
	if started {
		return nil // монитор уже запущен
	}
	// отдаем команду на запуск монитора на сервере MX
	resp, err := m.conn.SendWithResponse(&struct {
		XMLName xml.Name `xml:"MonitorStart"`
		Ext     string   `xml:"monitorObject>deviceObject"`
	}{
		Ext: ext,
	})
	if err != nil {
		return err
	}
	// разбираем идентификатор с номером запущенного монитора
	var monitor = new(struct {
		ID int64 `xml:"monitorCrossRefID"`
	})
	if err = resp.Decode(monitor); err != nil {
		return err
	}
	// сохраняем номер запущенного монитора и его ассоциацию с внутренним
	// номером пользователя и SSE-брокером.
	m.monitors.Store(monitor.ID, &monitorData{
		Extension: ext,
		Broker:    sse.New(),
	})
	return nil
}

// MonitorStop останавливает пользовательский монитор.
func (m *MXServer) MonitorStop(ext string) error {
	// находим идентификатор запущенного монитора пользователя
	var monitorID int64
	m.monitors.Range(func(mID, data interface{}) bool {
		var md = data.(*monitorData)
		if md.Extension != ext {
			return true
		}
		m.monitors.Delete(mID) // удаляем из списка
		md.Close()
		monitorID = mID.(int64)
		return false
	})
	if monitorID == 0 {
		return nil
	}
	// отправляем команду на остановку монитора
	_, err := m.conn.SendWithResponse(&struct {
		XMLName xml.Name `xml:"MonitorStop"`
		ID      int64    `xml:"monitorCrossRefID"`
	}{
		ID: monitorID,
	})
	return err
}

// MakeCall отправляет команду на серверный звонок MX.
func (m *MXServer) MakeCall(from, to string) (*MakeCallResponse, error) {
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
			Ext:  from,
		},
		To: to,
	}
	resp, err := m.conn.SendWithResponse(cmd)
	if err != nil {
		return nil, err
	}
	// разбираем ответ
	var result = new(MakeCallResponse)
	if err = resp.Decode(result); err != nil {
		return nil, err
	}
	return result, nil
}

// MakeCallResponse описывает информацию, возвращаемую сервером MX при запросе
// серверного звонка.
type MakeCallResponse struct {
	CallID       uint64 `xml:"callingDevice>callID" json:"callId"`
	DeviceID     string `xml:"callingDevice>deviceID" json:"deviceId"`
	CalledDevice string `xml:"calledDevice" json:"called"`
}

// monitoring запускает процесс мониторинга звонков.
func (m *MXServer) monitoring() error {
	// запускаем мониторинг изменений в адресной книге
	if _, err := m.conn.SendWithResponse("<MonitorStartAb/>"); err != nil {
		return err
	}
	// обрабатываем события с сервера
	return m.conn.Handle(func(resp *mx.Response) error {
		// обрабатываем события адресной книги
		switch resp.Name {
		case "AbUpdateUserEvent", "AbAddUserEvent":
			// добавление/изменения пользователя в адресной книге
			var update = new(struct {
				Contact *mx.Contact `xml:"abentry"`
			})
			if err := resp.Decode(update); err != nil {
				log.IfErr(err, "mx event parse error", "event", resp.Name)
				return nil
			}
			m.ab.Store(update.Contact.JID, update.Contact)
			log.Debug("contact updated", "jid", update.Contact.JID)
			return nil
		case "AbDeleteUserEvent":
			// удаление пользователя из адресной книги
			var update = new(struct {
				JID mx.JID `xml:"userId"`
			})
			if err := resp.Decode(update); err != nil {
				log.IfErr(err, "mx event parse error", "event", resp.Name)
				return nil
			}
			m.ab.Delete(update.JID)
			log.Debug("contact deleted", "jid", update.JID)
			return nil
		}

		// обрабатываем события о звонках
		var monitor = new(struct {
			ID int64 `xml:"monitorCrossRefID"`
		})
		if err := resp.Decode(monitor); err != nil {
			log.IfErr(err, "bad monitored event format")
			return nil
		}
		var mData *monitorData
		if data, ok := m.monitors.Load(monitor.ID); ok {
			mData = data.(*monitorData)
		} else {
			log.Warn("not monitored event")
			return nil
		}
		var event interface{} // данные для отсылки информации о событии
		switch resp.Name {
		case "OriginatedEvent":
			event = new(struct {
				CallID        int64  `xml:"originatedConnection>callID" json:"callId"`
				DeviceID      string `xml:"originatedConnection>deviceID" json:"deviceId"`
				CallingDevice string `xml:"callingDevice>deviceIdentifier" json:"callingDevice"`
				CalledDevice  string `xml:"calledDevice>deviceIdentifier" json:"calledDevice"`
				Cause         string `xml:"cause" json:"cause"`
				CallTypeFlags uint32 `xml:"callTypeFlags" json:"callTypeFlags,omitempty"`
				CmdsAllowed   uint32 `xml:"cmdsAllowed" json:"cmdsAllowed,omitempty"`
			})
		case "DivertedEvent":
			event = new(struct {
				CallID                int64  `xml:"connection>callID" json:"callId"`
				DeviceID              string `xml:"connection>deviceID" json:"deviceId"`
				DivertingDevice       string `xml:"divertingDevice>deviceIdentifier" json:"divertingDevice"`
				NewDestination        string `xml:"newDestination>deviceIdentifier" json:"newDestination"`
				LastRedirectionDevice string `xml:"lastRedirectionDevice>deviceIdentifier" json:"lastRedirectionDevice,omitempty"`
				Cause                 string `xml:"cause" json:"cause"`
				CallTypeFlags         uint32 `xml:"callTypeFlags" json:"callTypeFlags,omitempty"`
				CmdsAllowed           uint32 `xml:"cmdsAllowed" json:"cmdsAllowed,omitempty"`
			})
		case "DeliveredEvent":
			event = new(struct {
				CallID                int64  `xml:"connection>callID" json:"callId"`
				DeviceID              string `xml:"connection>deviceID" json:"deviceId"`
				GlobalCallID          string `xml:"connection>globalCallID" json:"globalCallId"`
				AlertingDevice        string `xml:"alertingDevice>deviceIdentifier" json:"alertingDevice"`
				CallingDevice         string `xml:"callingDevice>deviceIdentifier" json:"callingDevice"`
				CalledDevice          string `xml:"calledDevice>deviceIdentifier" json:"calledDevice"`
				LastRedirectionDevice string `xml:"lastRedirectionDevice>deviceIdentifier" json:"lastRedirectionDevice,omitempty"`
				LocalConnectionInfo   string `xml:"localConnectionInfo" json:"localConnectionInfo"`
				Cause                 string `xml:"cause" json:"cause"`
				CallTypeFlags         uint32 `xml:"callTypeFlags" json:"callTypeFlags,omitempty"`
				CmdsAllowed           uint32 `xml:"cmdsAllowed" json:"cmdsAllowed,omitempty"`
				Cads                  []struct {
					Name  string `xml:"name,attr" json:"name"`
					Type  string `xml:"type,attr" json:"type"`
					Value string `xml:",chardata" json:"value,omitempty"`
				} `xml:"cad,omitempty" json:"cads,omitempty"`
			})
		case "EstablishedEvent":
			event = new(struct {
				CallID                int64  `xml:"establishedConnection>callID" json:"callId"`
				DeviceID              string `xml:"establishedConnection>deviceID" json:"deviceId"`
				GlobalCallID          string `xml:"establishedConnection>globalCallID" json:"globalCallId"`
				AnsweringDevice       string `xml:"answeringDevice>deviceIdentifier" json:"answeringDevice"`
				AnsweringDisplayName  string `xml:"answeringDisplayName" json:"answeringDisplayName"`
				CallingDevice         string `xml:"callingDevice>deviceIdentifier" json:"callingDevice"`
				CalledDevice          string `xml:"calledDevice>deviceIdentifier" json:"calledDevice"`
				LastRedirectionDevice string `xml:"lastRedirectionDevice>deviceIdentifier" json:"lastRedirectionDevice,omitempty"`
				CallingDisplayName    string `xml:"callingDisplayName" json:"callingDisplayName"`
				Cause                 string `xml:"cause" json:"cause"`
				CallTypeFlags         uint32 `xml:"callTypeFlags" json:"callTypeFlags,omitempty"`
				CmdsAllowed           uint32 `xml:"cmdsAllowed" json:"cmdsAllowed,omitempty"`
				Cads                  []struct {
					Name  string `xml:"name,attr" json:"name"`
					Type  string `xml:"type,attr" json:"type"`
					Value string `xml:",chardata" json:"value,omitempty"`
				} `xml:"cad,omitempty" json:"cads,omitempty"`
			})
		case "ConnectionClearedEvent":
			event = new(struct {
				CallID          int64  `xml:"droppedConnection>callID" json:"callId"`
				DeviceID        string `xml:"droppedConnection>deviceID" json:"deviceId"`
				ReleasingDevice string `xml:"releasingDevice>deviceIdentifier" json:"releasingDevice"`
				Cause           string `xml:"cause" json:"cause"`
			})
		}
		if log.IfErr(resp.Decode(event), "event decode error") != nil {
			return nil
		}
		data, err := json.Marshal(event)
		if log.IfErr(err, "json encode event error") != nil {
			return nil
		}
		mData.Data(resp.Name, string(data), "") // отсылаем данные
		log.Info("monitoring event",
			"event", resp.Name,
			"ext", mData.Extension,
			"monitors", mData.Connected())
		return nil
	}, "AbUpdateUserEvent", "AbAddUserEvent", "AbDeleteUserEvent",
		"OriginatedEvent", "DivertedEvent", "DeliveredEvent",
		"EstablishedEvent", "ConnectionClearedEvent")
}

// ConnectionInfo возвращает информацию о мониторинге и количестве подключений
// к станице с событиями.
func (m *MXServer) ConnectionInfo() map[string]int {
	var result = make(map[string]int)
	m.monitors.Range(func(mID, data interface{}) bool {
		var md = data.(*monitorData)
		result[md.Extension] = md.Connected()
		return true
	})
	if len(result) == 0 {
		return nil
	}
	return result
}

// Contacts возвращает список контактов.
func (m *MXServer) Contacts() []*mx.Contact {
	var list []*mx.Contact
	m.ab.Range(func(_, contact interface{}) bool {
		list = append(list, contact.(*mx.Contact))
		return true
	})
	sort.Slice(list, func(i, j int) bool {
		return list[i].Ext < list[j].Ext
	})
	return list
}

// // CallHold подвешивает звонок.
// func (m *MXServer) CallHold(callID uint64, deviceID string) error {
// 	var cmd = &struct {
// 		XMLName  xml.Name `xml:"HoldCall"`
// 		CallID   uint64   `xml:"callToBeHeld>callID"`
// 		DeviceID string   `xml:"callToBeHeld>deviceID"`
// 	}{
// 		CallID:   callID,
// 		DeviceID: deviceID,
// 	}
// 	_, err := m.conn.SendWithResponse(cmd)
// 	return err
// }

// CallHangup сбрасывает звонок.
func (m *MXServer) CallHangup(callID uint64, deviceID string) error {
	var cmd = &struct {
		XMLName  xml.Name `xml:"ClearConnection"`
		CallID   uint64   `xml:"connectionToBeCleared>callID"`
		DeviceID string   `xml:"connectionToBeCleared>deviceID"`
	}{
		CallID:   callID,
		DeviceID: deviceID,
	}
	return m.conn.Send(cmd)
}

// CallTransfer перебрасывает звонок.
func (m *MXServer) CallTransfer(callID uint64, deviceID, destination string) error {
	var cmd = &struct {
		XMLName        xml.Name `xml:"SingleStepTransferCall"`
		CallID         uint64   `xml:"activeCall>callID"`
		DeviceID       string   `xml:"activeCall>deviceID"`
		NewDestination string   `xml:"transferredTo"`
	}{
		CallID:         callID,
		DeviceID:       deviceID,
		NewDestination: destination,
	}
	return m.conn.Send(cmd)
}
