// gowm.go
//
// Copyright (c) 2021-2022 Kristofer Berggren
// All rights reserved.
//
// nchat is distributed under the MIT license, see LICENSE for details.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store"

	"github.com/mdp/qrterminal"

	_ "github.com/mattn/go-sqlite3"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/libsignal/logger"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

var whatsmeowDate = "20220426"

type JSONMessage []json.RawMessage
type JSONMessageType string

type intString struct {
	i int
	s string
}

type State int64

const (
	None State = iota
	Connecting
	Connected
	Disconnected
)

var (
	mx         sync.Mutex
	clients    map[int]*whatsmeow.Client = make(map[int]*whatsmeow.Client)
	paths      map[int]string            = make(map[int]string)
	states     map[int]State             = make(map[int]State)
	timeUnread map[intString]int         = make(map[intString]int)
	handlers   map[int]*WmEventHandler   = make(map[int]*WmEventHandler)
)

// keep in sync with enum FileStatus in protocol.h
var FileStatusNone = -1
var FileStatusNotDownloaded = 0
var FileStatusDownloaded = 1
var FileStatusDownloading = 2
var FileStatusDownloadFailed = 3

func AddConn(conn *whatsmeow.Client, path string) int {
	mx.Lock()
	var connId int = len(clients)
	clients[connId] = conn
	paths[connId] = path
	states[connId] = None
	handlers[connId] = &WmEventHandler{connId}
	mx.Unlock()
	return connId
}

func GetClient(connId int) *whatsmeow.Client {
	mx.Lock()
	var client *whatsmeow.Client = clients[connId]
	mx.Unlock()
	return client
}

func GetHandler(connId int) *WmEventHandler {
	mx.Lock()
	var handler *WmEventHandler = handlers[connId]
	mx.Unlock()
	return handler
}

func GetPath(connId int) string {
	mx.Lock()
	var path string = paths[connId]
	mx.Unlock()
	return path
}

func GetState(connId int) State {
	mx.Lock()
	var state State = states[connId]
	mx.Unlock()
	return state
}

func SetState(connId int, status State) {
	mx.Lock()
	states[connId] = status
	mx.Unlock()
}

func RemoveConn(connId int) {
	mx.Lock()
	delete(clients, connId)
	delete(paths, connId)
	delete(handlers, connId)
	mx.Unlock()
}

// utils
func ShowImage(path string) {
	switch runtime.GOOS {
	case "linux":
		LOG_DEBUG("xdg-open " + path)
		exec.Command("xdg-open", path).Start()
	case "darwin":
		LOG_DEBUG("open " + path)
		exec.Command("open", path).Start()
	default:
		LOG_WARNING("unsupported os")
	}
}

func HasGUI() bool {
	switch runtime.GOOS {
	case "linux":
		_, displaySet := os.LookupEnv("DISPLAY")
		return displaySet
	case "darwin":
		return true
	default:
		return true
	}
}

func BoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func IntToBool(i int) bool {
	return i != 0
}

func StringToInt(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return i
}

func JidToStr(jid types.JID) string {
	return jid.User + "@" + jid.Server
}

// based on https://github.com/mautrix/whatsapp/blob/master/historysync.go
func ParseWebMessageInfo(selfJid types.JID, chatJid types.JID, webMsg *waProto.WebMessageInfo) *types.MessageInfo {
	info := types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chatJid,
			IsFromMe: webMsg.GetKey().GetFromMe(),
			IsGroup:  chatJid.Server == types.GroupServer,
		},
		ID:        webMsg.GetKey().GetId(),
		PushName:  webMsg.GetPushName(),
		Timestamp: time.Unix(int64(webMsg.GetMessageTimestamp()), 0),
	}
	if info.IsFromMe {
		info.Sender = selfJid.ToNonAD()
	} else if webMsg.GetParticipant() != "" {
		info.Sender, _ = types.ParseJID(webMsg.GetParticipant())
	} else if webMsg.GetKey().GetParticipant() != "" {
		info.Sender, _ = types.ParseJID(webMsg.GetKey().GetParticipant())
	} else {
		info.Sender = chatJid
	}
	if info.Sender.IsEmpty() {
		return nil
	}
	return &info
}

// logger
type ncLogger struct{}

func (s *ncLogger) Debugf(msg string, args ...interface{}) {
	LOG_DEBUG(fmt.Sprintf("whatsmeow %s", fmt.Sprintf(msg, args...)))
}

func (s *ncLogger) Infof(msg string, args ...interface{}) {
	LOG_INFO(fmt.Sprintf("whatsmeow %s", fmt.Sprintf(msg, args...)))
}

func (s *ncLogger) Warnf(msg string, args ...interface{}) {
	LOG_WARNING(fmt.Sprintf("whatsmeow %s", fmt.Sprintf(msg, args...)))
}

func (s *ncLogger) Errorf(msg string, args ...interface{}) {
	LOG_ERROR(fmt.Sprintf("whatsmeow %s", fmt.Sprintf(msg, args...)))
}

func (s *ncLogger) Sub(mod string) waLog.Logger {
	return s
}

func NcLogger() waLog.Logger {
	return &ncLogger{}
}

// loggable
type ncSignalLogger struct{}

func (s *ncSignalLogger) Debug(caller, msg string) {
	LOG_DEBUG(fmt.Sprintf("whatsmeow %s", fmt.Sprintf("%s %s", caller, msg)))
}

func (s *ncSignalLogger) Info(caller, msg string) {
	LOG_INFO(fmt.Sprintf("whatsmeow %s", fmt.Sprintf("%s %s", caller, msg)))
}

func (s *ncSignalLogger) Warning(caller, msg string) {
	LOG_WARNING(fmt.Sprintf("whatsmeow %s", fmt.Sprintf("%s %s", caller, msg)))
}

func (s *ncSignalLogger) Error(caller, msg string) {
	LOG_ERROR(fmt.Sprintf("whatsmeow %s", fmt.Sprintf("%s %s", caller, msg)))
}

func (s *ncSignalLogger) Configure(ss string) {
}

// event handling - based on:
// https://github.com/hoehermann/purple-gowhatsapp/blob/whatsmeow/src/go/handler.go
// https://github.com/tulir/whatsmeow/blob/main/mdtest/main.go
type WmEventHandler struct {
	connId int
}

func (handler *WmEventHandler) HandleEvent(rawEvt interface{}) {
	switch evt := rawEvt.(type) {

	case *events.AppStateSyncComplete:
		// this happens after initial logon via QR code
		LOG_DEBUG(fmt.Sprintf("%#v", evt))
		if evt.Name == appstate.WAPatchCriticalBlock {
			LOG_DEBUG("AppStateSyncComplete and WAPatchCriticalBlock")
			handler.HandleConnected()
		}

	case *events.PushNameSetting:
		// send presence when the pushname is changed remotely
		LOG_DEBUG(fmt.Sprintf("%#v", evt))
		handler.HandleConnected()

	case *events.PushName:
		// other device changed our friendly name
		LOG_DEBUG(fmt.Sprintf("%#v", evt))

	case *events.Connected:
		// connected
		LOG_DEBUG(fmt.Sprintf("%#v", evt))
		handler.HandleConnected()
		SetState(handler.connId, Connected)

	case *events.StreamReplaced:
		// TODO: find out when exactly this happens and how to handle it
		LOG_DEBUG(fmt.Sprintf("%#v", evt))

	case *events.Message:
		LOG_DEBUG(fmt.Sprintf("%#v", evt))
		handler.HandleMessage(evt.Info, evt.Message, false)

	case *events.Receipt:
		LOG_DEBUG(fmt.Sprintf("%#v", evt))
		handler.HandleReceipt(evt)

	case *events.Presence:
		LOG_DEBUG(fmt.Sprintf("%#v", evt))
		handler.HandlePresence(evt)

	case *events.ChatPresence:
		LOG_DEBUG(fmt.Sprintf("%#v", evt))
		handler.HandleChatPresence(evt)

	case *events.HistorySync:
		// This happens after initial logon via QR code (after AppStateSyncComplete)
		LOG_DEBUG(fmt.Sprintf("%#v", evt))
		handler.HandleHistorySync(evt)

	case *events.AppState:
		LOG_DEBUG(fmt.Sprintf("%#v - %+v / %+v", evt, evt.Index, evt.SyncActionValue))

	case *events.LoggedOut:
		// logged out, need re-init?
		LOG_DEBUG(fmt.Sprintf("%#v", evt))

	case *events.QR:
		// handled in WmLogin
		LOG_DEBUG(fmt.Sprintf("%#v", evt))

	case *events.PairSuccess:
		LOG_DEBUG(fmt.Sprintf("%#v", evt))

	case *events.JoinedGroup:
		LOG_DEBUG(fmt.Sprintf("%#v", evt))

	case *events.OfflineSyncCompleted:
		LOG_DEBUG(fmt.Sprintf("%#v", evt))
		handler.GetContacts()

	default:
		LOG_DEBUG(fmt.Sprintf("Event type not handled: %#v", rawEvt))
	}
}

func (handler *WmEventHandler) HandleConnected() {
	LOG_TRACE(fmt.Sprintf("HandleConnected"))
	var client *whatsmeow.Client = GetClient(handler.connId)

	if len(client.Store.PushName) == 0 {
		return
	}

	err := client.SendPresence(types.PresenceAvailable)
	if err != nil {
		LOG_WARNING("Failed to send available presence")
	} else {
		LOG_TRACE("Marked self as available")
	}
}

func (handler *WmEventHandler) HandleReceipt(receipt *events.Receipt) {
	if receipt.Type == events.ReceiptTypeRead || receipt.Type == events.ReceiptTypeReadSelf {
		LOG_TRACE(fmt.Sprintf("%v was read by %s at %s", receipt.MessageIDs, receipt.SourceString(), receipt.Timestamp))
		connId := handler.connId
		chatId := receipt.SourceString()
		isRead := true
		for _, msgId := range receipt.MessageIDs {
			LOG_DEBUG(fmt.Sprintf("Call CWmNewMessageStatusNotify"))
			CWmNewMessageStatusNotify(connId, chatId, msgId, BoolToInt(isRead))
		}
	}
}

func (handler *WmEventHandler) HandlePresence(presence *events.Presence) {
	connId := handler.connId
	chatId := ""
	userId := presence.From.ToNonAD().String()
	isOnline := !presence.Unavailable
	isTyping := false
	LOG_DEBUG(fmt.Sprintf("Call CWmNewStatusNotify"))
	CWmNewStatusNotify(connId, chatId, userId, BoolToInt(isOnline), BoolToInt(isTyping))
}

func (handler *WmEventHandler) HandleChatPresence(chatPresence *events.ChatPresence) {
	connId := handler.connId
	chatId := chatPresence.MessageSource.Chat.ToNonAD().String()
	userId := chatPresence.MessageSource.Sender.ToNonAD().String()
	isOnline := true
	isTyping := (chatPresence.State == types.ChatPresenceComposing)
	LOG_DEBUG(fmt.Sprintf("Call CWmNewStatusNotify"))
	CWmNewStatusNotify(connId, chatId, userId, BoolToInt(isOnline), BoolToInt(isTyping))
}

func (handler *WmEventHandler) HandleHistorySync(historySync *events.HistorySync) {
	var client *whatsmeow.Client = GetClient(handler.connId)
	selfJid := *client.Store.ID

	LOG_DEBUG(fmt.Sprintf("HandleHistorySync SyncType %#v", *historySync.Data.SyncType))

	pushnames := historySync.Data.GetPushnames()
	for _, pushname := range pushnames {
		if pushname.Id != nil && pushname.Pushname != nil {
			LOG_DEBUG(fmt.Sprintf("HandleHistorySync Pushname %s %s", *pushname.Id, *pushname.Pushname))
		}
	}

	conversations := historySync.Data.GetConversations()
	for _, conversation := range conversations {
		LOG_DEBUG(fmt.Sprintf("HandleHistorySync Conversation %#v", *conversation))

		chatJid, _ := types.ParseJID(conversation.GetId())

		isUnread := 0
		isMuted := 0
		lastMessageTime := 0

		LOG_DEBUG(fmt.Sprintf("Call CWmNewChatsNotify %s", JidToStr(chatJid)))
		CWmNewChatsNotify(handler.connId, JidToStr(chatJid), isUnread, isMuted, lastMessageTime)

		syncMessages := conversation.GetMessages()
		for _, syncMessage := range syncMessages {
			webMessageInfo := syncMessage.Message
			messageInfo := ParseWebMessageInfo(selfJid, chatJid, webMessageInfo)
			message := webMessageInfo.GetMessage()

			if (messageInfo == nil) || (message == nil) {
				continue
			}

			handler.HandleMessage(*messageInfo, message, true)
		}
	}
}

func (handler *WmEventHandler) GetContacts() {
	var client *whatsmeow.Client = GetClient(handler.connId)
	connId := handler.connId
	LOG_DEBUG(fmt.Sprintf("GetContacts"))

	contacts, contErr := client.Store.Contacts.GetAllContacts()
	if contErr != nil {
		LOG_WARNING("contact tmperror")
	}

	LOG_DEBUG(fmt.Sprintf("contacts %+v", contacts))

	for jid, contactInfo := range contacts {
		LOG_DEBUG(fmt.Sprintf("Call CWmNewContactsNotify %s %s", JidToStr(jid), contactInfo.PushName))
		CWmNewContactsNotify(connId, JidToStr(jid), contactInfo.PushName, BoolToInt(false))
	}

	// special handling for self
	selfId := JidToStr(*client.Store.ID)
	selfName := "" // overridden by ui

	LOG_DEBUG(fmt.Sprintf("Call CWmNewContactsNotify %s %s", selfId, selfName))
	CWmNewContactsNotify(connId, selfId, selfName, BoolToInt(true))

	groups, groupErr := client.GetJoinedGroups()
	LOG_DEBUG(fmt.Sprintf("groups %+v", groups))
	if groupErr != nil {
		LOG_WARNING(fmt.Sprintf("error %v", groupErr))
	} else {
		for _, group := range groups {
			LOG_DEBUG(fmt.Sprintf("Call CWmNewContactsNotify %s %s", JidToStr(group.JID), group.GroupName.Name))
			CWmNewContactsNotify(connId, JidToStr(group.JID), group.GroupName.Name, BoolToInt(false))
		}
	}
}

func (handler *WmEventHandler) HandleMessage(messageInfo types.MessageInfo, msg *waProto.Message, isSync bool) {
	switch {
	case msg.Conversation != nil || msg.ExtendedTextMessage != nil:
		handler.HandleTextMessage(messageInfo, msg, isSync)

	case msg.ImageMessage != nil:
		handler.HandleImageMessage(messageInfo, msg, isSync)

	case msg.VideoMessage != nil:
		handler.HandleVideoMessage(messageInfo, msg, isSync)

	case msg.AudioMessage != nil:
		handler.HandleAudioMessage(messageInfo, msg, isSync)

	case msg.DocumentMessage != nil:
		handler.HandleDocumentMessage(messageInfo, msg, isSync)
	}
}

func (handler *WmEventHandler) HandleTextMessage(messageInfo types.MessageInfo, msg *waProto.Message, isSync bool) {
	LOG_TRACE(fmt.Sprintf("TextMessage"))

	connId := handler.connId
	chatId := JidToStr(messageInfo.Chat)
	msgId := messageInfo.ID
	text := ""

	quotedId := ""
	if msg.GetExtendedTextMessage() == nil {
		text = msg.GetConversation()
	} else {
		text = msg.GetExtendedTextMessage().GetText()
		ci := msg.GetExtendedTextMessage().GetContextInfo()
		if ci != nil {
			quotedId = ci.GetStanzaId()
		}
	}

	fromMe := messageInfo.IsFromMe
	senderId := JidToStr(messageInfo.Sender)
	filePath := ""
	fileStatus := FileStatusNone

	timeSent := int(messageInfo.Timestamp.Unix())
	isSeen := isSync
	isOld := (timeSent <= timeUnread[intString{i: connId, s: chatId}])
	isRead := (fromMe && isSeen) || (!fromMe && isOld)

	UpdateTypingStatus(connId, chatId, senderId, fromMe, isOld)

	LOG_DEBUG(fmt.Sprintf("Call CWmNewMessagesNotify %s: %s", chatId, text))
	CWmNewMessagesNotify(connId, chatId, msgId, senderId, text, BoolToInt(fromMe), quotedId, filePath, fileStatus, timeSent, BoolToInt(isRead))
}

func (handler *WmEventHandler) HandleImageMessage(messageInfo types.MessageInfo, msg *waProto.Message, isSync bool) {
	LOG_TRACE(fmt.Sprintf("ImageMessage"))

	connId := handler.connId
	var client *whatsmeow.Client = GetClient(handler.connId)
	
	// get image part
	img := msg.GetImageMessage()
	if img == nil {
		LOG_ERROR(fmt.Sprintf("tmp err"))
		return
	}

	// get extension
	ext := "jpg"
	exts, extErr := mime.ExtensionsByType(img.GetMimetype())
	if extErr != nil && len(exts) > 0 {
		ext = exts[0]
	}

	// context
	quotedId := ""
	ci := img.GetContextInfo()
	if ci != nil {
		quotedId = ci.GetStanzaId()
	}
	
	// get temp file path
	var tmpPath string = GetPath(connId) + "/tmp"
	filePath := fmt.Sprintf("%v/%v.%v", tmpPath, messageInfo.ID, ext)
	fileStatus := FileStatusNone

	// download if not yet present
	if _, statErr := os.Stat(filePath); os.IsNotExist(statErr) {
		LOG_TRACE(fmt.Sprintf("ImageMessage new %v", filePath))
		data, err := client.Download(img)
		if err != nil {
			LOG_WARNING(fmt.Sprintf("download error %+v", err))
			fileStatus = FileStatusDownloadFailed
		} else {
			file, err := os.Create(filePath)
			defer file.Close()
			if err != nil {
				LOG_WARNING(fmt.Sprintf("create error %+v", err))
				fileStatus = FileStatusDownloadFailed
			} else {
				_, err = file.Write(data)
				if err != nil {
					LOG_WARNING(fmt.Sprintf("write error %+v", err))
					fileStatus = FileStatusDownloadFailed
				} else {
					LOG_TRACE(fmt.Sprintf("download ok"))
					fileStatus = FileStatusDownloaded
				}
			}
		}
	} else {
		LOG_TRACE(fmt.Sprintf("ImageMessage cached %v", filePath))
		fileStatus = FileStatusDownloaded
	}

	chatId := JidToStr(messageInfo.Chat)
	msgId := messageInfo.ID
	fromMe := messageInfo.IsFromMe
	senderId := JidToStr(messageInfo.Sender)
	text := img.GetCaption()

	timeSent := int(messageInfo.Timestamp.Unix())
	isSeen := isSync
	isOld := (timeSent <= timeUnread[intString{i: connId, s: chatId}])
	isRead := (fromMe && isSeen) || (!fromMe && isOld)

	UpdateTypingStatus(connId, chatId, senderId, fromMe, isOld)

	CWmNewMessagesNotify(connId, chatId, msgId, senderId, text, BoolToInt(fromMe), quotedId, filePath, fileStatus, timeSent, BoolToInt(isRead))
}

func (handler *WmEventHandler) HandleVideoMessage(messageInfo types.MessageInfo, msg *waProto.Message, isSync bool) {
	LOG_TRACE(fmt.Sprintf("VideoMessage"))

	connId := handler.connId
	var client *whatsmeow.Client = GetClient(handler.connId)

	// get video part
	vid := msg.GetVideoMessage()
	if vid == nil {
		LOG_ERROR(fmt.Sprintf("tmp err"))
		return
	}

	// get extension
	ext := "mp4"
	exts, extErr := mime.ExtensionsByType(vid.GetMimetype())
	if extErr != nil && len(exts) > 0 {
		ext = exts[0]
	}

	// context
	quotedId := ""
	ci := vid.GetContextInfo()
	if ci != nil {	
		quotedId = ci.GetStanzaId()
	}
	
	// get temp file path
	var tmpPath string = GetPath(connId) + "/tmp"
	filePath := fmt.Sprintf("%v/%v.%v", tmpPath, messageInfo.ID, ext)
	fileStatus := FileStatusNone

	if _, statErr := os.Stat(filePath); os.IsNotExist(statErr) {
		LOG_TRACE(fmt.Sprintf("VideoMessage new %v", filePath))
		data, err := client.Download(vid)
		if err != nil {
			LOG_WARNING(fmt.Sprintf("download error %+v", err))
			fileStatus = FileStatusDownloadFailed
		} else {
			file, err := os.Create(filePath)
			defer file.Close()
			if err != nil {
				LOG_WARNING(fmt.Sprintf("create error %+v", err))
				fileStatus = FileStatusDownloadFailed
			} else {
				_, err = file.Write(data)
				if err != nil {
					LOG_WARNING(fmt.Sprintf("write error %+v", err))
					fileStatus = FileStatusDownloadFailed
				} else {
					LOG_TRACE(fmt.Sprintf("download ok"))
					fileStatus = FileStatusDownloaded
				}
			}
		}
	} else {
		LOG_TRACE(fmt.Sprintf("VideoMessage cached %v", filePath))
		fileStatus = FileStatusDownloaded
	}

	chatId := JidToStr(messageInfo.Chat)
	msgId := messageInfo.ID
	fromMe := messageInfo.IsFromMe
	senderId := JidToStr(messageInfo.Sender)
	text := vid.GetCaption()

	timeSent := int(messageInfo.Timestamp.Unix())
	isSeen := isSync
	isOld := (timeSent <= timeUnread[intString{i: connId, s: chatId}])
	isRead := (fromMe && isSeen) || (!fromMe && isOld)

	UpdateTypingStatus(connId, chatId, senderId, fromMe, isOld)

	CWmNewMessagesNotify(connId, chatId, msgId, senderId, text, BoolToInt(fromMe), quotedId, filePath, fileStatus, timeSent, BoolToInt(isRead))
}

func (handler *WmEventHandler) HandleAudioMessage(messageInfo types.MessageInfo, msg *waProto.Message, isSync bool) {
	LOG_TRACE(fmt.Sprintf("AudioMessage"))

	connId := handler.connId
	var client *whatsmeow.Client = GetClient(handler.connId)

	// get audio part
	aud := msg.GetAudioMessage()
	if aud == nil {
		LOG_ERROR(fmt.Sprintf("tmp err"))
		return
	}

	// get extension
	ext := "ogg"
	exts, extErr := mime.ExtensionsByType(aud.GetMimetype())
	if extErr != nil && len(exts) > 0 {
		ext = exts[0]
	}

	// context
	quotedId := ""
	ci := aud.GetContextInfo()
	if ci != nil {
		quotedId = ci.GetStanzaId()
	}
	
	// get temp file path
	var tmpPath string = GetPath(connId) + "/tmp"
	filePath := fmt.Sprintf("%v/%v.%v", tmpPath, messageInfo.ID, ext)
	fileStatus := FileStatusNone

	if _, statErr := os.Stat(filePath); os.IsNotExist(statErr) {
		LOG_TRACE(fmt.Sprintf("AudioMessage new %v", filePath))
		data, err := client.Download(aud)
		if err != nil {
			LOG_WARNING(fmt.Sprintf("download error %+v", err))
			fileStatus = FileStatusDownloadFailed
		} else {
			file, err := os.Create(filePath)
			defer file.Close()
			if err != nil {
				LOG_WARNING(fmt.Sprintf("create error %+v", err))
				fileStatus = FileStatusDownloadFailed
			} else {
				_, err = file.Write(data)
				if err != nil {
					LOG_WARNING(fmt.Sprintf("write error %+v", err))
					fileStatus = FileStatusDownloadFailed
				} else {
					LOG_TRACE(fmt.Sprintf("download ok"))
					fileStatus = FileStatusDownloaded
				}
			}
		}
	} else {
		LOG_TRACE(fmt.Sprintf("AudioMessage cached %v", filePath))
		fileStatus = FileStatusDownloaded
	}

	chatId := JidToStr(messageInfo.Chat)
	msgId := messageInfo.ID
	fromMe := messageInfo.IsFromMe
	senderId := JidToStr(messageInfo.Sender)
	text := ""

	timeSent := int(messageInfo.Timestamp.Unix())
	isSeen := isSync
	isOld := (timeSent <= timeUnread[intString{i: connId, s: chatId}])
	isRead := (fromMe && isSeen) || (!fromMe && isOld)

	UpdateTypingStatus(connId, chatId, senderId, fromMe, isOld)

	CWmNewMessagesNotify(connId, chatId, msgId, senderId, text, BoolToInt(fromMe), quotedId, filePath, fileStatus, timeSent, BoolToInt(isRead))
}

func (handler *WmEventHandler) HandleDocumentMessage(messageInfo types.MessageInfo, msg *waProto.Message, isSync bool) {
	LOG_TRACE(fmt.Sprintf("DocumentMessage"))

	connId := handler.connId
	var client *whatsmeow.Client = GetClient(handler.connId)

	// get doc part
	doc := msg.GetDocumentMessage()
	if doc == nil {
		LOG_ERROR(fmt.Sprintf("tmp err"))
		return
	}

	// context
	quotedId := ""
	ci := doc.GetContextInfo()
	if ci != nil {
		quotedId = ci.GetStanzaId()
	}
	
	// get temp file path
	var tmpPath string = GetPath(connId) + "/tmp"
	filePath := fmt.Sprintf("%v/%v-%s", tmpPath, messageInfo.ID, *doc.FileName)
	fileStatus := FileStatusNone

	if _, statErr := os.Stat(filePath); os.IsNotExist(statErr) {
		LOG_TRACE(fmt.Sprintf("DocumentMessage new %v", filePath))
		data, err := client.Download(doc)
		if err != nil {
			LOG_WARNING(fmt.Sprintf("download error %+v", err))
			fileStatus = FileStatusDownloadFailed
		} else {
			file, err := os.Create(filePath)
			defer file.Close()
			if err != nil {
				LOG_WARNING(fmt.Sprintf("create error %+v", err))
				fileStatus = FileStatusDownloadFailed
			} else {
				_, err = file.Write(data)
				if err != nil {
					LOG_WARNING(fmt.Sprintf("write error %+v", err))
					fileStatus = FileStatusDownloadFailed
				} else {
					LOG_TRACE(fmt.Sprintf("download ok"))
					fileStatus = FileStatusDownloaded
				}
			}
		}
	} else {
		LOG_TRACE(fmt.Sprintf("DocumentMessage cached %v", filePath))
		fileStatus = FileStatusDownloaded
	}

	chatId := JidToStr(messageInfo.Chat)
	msgId := messageInfo.ID
	fromMe := messageInfo.IsFromMe
	senderId := JidToStr(messageInfo.Sender)
	text := ""

	timeSent := int(messageInfo.Timestamp.Unix())
	isSeen := isSync
	isOld := (timeSent <= timeUnread[intString{i: connId, s: chatId}])
	isRead := (fromMe && isSeen) || (!fromMe && isOld)

	UpdateTypingStatus(connId, chatId, senderId, fromMe, isOld)

	CWmNewMessagesNotify(connId, chatId, msgId, senderId, text, BoolToInt(fromMe), quotedId, filePath, fileStatus, timeSent, BoolToInt(isRead))
}

func UpdateTypingStatus(connId int, chatId string, userId string, fromMe bool, isOld bool) {

	// only handle new messages from others
	if fromMe || isOld {
		return
	}

	LOG_TRACE("update typing status " + strconv.Itoa(connId) + ", " + chatId + ", " + userId)

	// sanity check arg
	if connId == -1 {
		LOG_WARNING("invalid connId")
	}

	// update
	isOnline := true
	isTyping := false

	LOG_DEBUG(fmt.Sprintf("Call CWmNewStatusNotify"))
	CWmNewStatusNotify(connId, chatId, userId, BoolToInt(isOnline), BoolToInt(isTyping))
}

func WmInit(path string) int {

	LOG_DEBUG("init " + filepath.Base(path))

	// create tmp dir
	var tmpPath string = path + "/tmp"
	tmpErr := os.MkdirAll(tmpPath, os.ModePerm)
	if tmpErr != nil {
		LOG_WARNING(fmt.Sprintf("mkdir error %+v", tmpErr))
		return -1
	}

	var ncLogger logger.Loggable = &ncSignalLogger{}
	logger.Setup(&ncLogger)

	dbLog := NcLogger()
	sessionPath := path + "/session.db"
	sqlAddress := fmt.Sprintf("file:%s?_foreign_keys=on", sessionPath)
	container, sqlErr := sqlstore.New("sqlite3", sqlAddress, dbLog)
	if sqlErr != nil {
		LOG_WARNING(fmt.Sprintf("sqlite error %+v", sqlErr))
		return -1
	}

	deviceStore, devErr := container.GetFirstDevice()
	if devErr != nil {
		LOG_WARNING(fmt.Sprintf("dev store error %+v", devErr))
		return -1
	}

	requireFullSync := true
	store.CompanionProps.RequireFullSync = &requireFullSync
	store.CompanionProps.PlatformType = waProto.CompanionProps_FIREFOX.Enum()
	switch runtime.GOOS {
	case "linux":
		store.CompanionProps.Os = proto.String("Linux")
	case "darwin":
		store.CompanionProps.Os = proto.String("Mac OS")
	default:
	}

	// create new whatsapp connection
	clientLog := NcLogger()
	client := whatsmeow.NewClient(deviceStore, clientLog)
	if client == nil {
		LOG_WARNING("client error")
		return -1
	}

	// store connection and get id
	var connId int = AddConn(client, path)

	LOG_DEBUG("connId " + strconv.Itoa(connId))

	return connId
}

func WmLogin(connId int) int {

	LOG_DEBUG("login " + strconv.Itoa(connId) + " whatsmeow " + whatsmeowDate)

	// sanity check arg
	if connId == -1 {
		LOG_WARNING("invalid connId")
		return -1
	}

	// get path and conn
	var path string = GetPath(connId)
	var cli *whatsmeow.Client = GetClient(connId)

	// authenticate if needed, otherwise just connect
	SetState(connId, Connecting)

	ch, err := cli.GetQRChannel(context.Background())
	if err != nil {
		// This error means that we're already logged in, so ignore it.
		if !errors.Is(err, whatsmeow.ErrQRStoreContainsID) {
			//log.Errorf("Failed to get QR channel: %v", err)
			LOG_WARNING("tmp")
		}
	} else {
		go func() {
			for evt := range ch {
				if evt.Event == "code" {
					if HasGUI() {
						qrPath := path + "/tmp/qr.png"
						qrcode.WriteFile(evt.Code, qrcode.Medium, 512, qrPath)
						ShowImage(qrPath)
					} else {
						qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
					}
				} else {
					//log.Infof("QR channel result: %s", evt.Event)
					LOG_WARNING("tmp")
				}
			}
		}()
	}

	eventHandler := GetHandler(connId)
	cli.AddEventHandler(eventHandler.HandleEvent)
	err = cli.Connect()
	if err != nil {
		LOG_WARNING("tmp")
		//log.Errorf("Failed to connect: %v", err)
		return -1
	}

	LOG_DEBUG("conn ok")

	// wait for result (up to 10 sec, 100 ms at a time)
	LOG_WARNING("wait start")
	i := 0
	for (i < 100) && (GetState(connId) == Connecting) {
		time.Sleep(100 * time.Millisecond)
		i++
	}
	LOG_WARNING("wait done")

	// delete temporary image file
	_ = os.Remove(path + "/tmp/qr.png")

	//XXX
	if GetState(connId) != Connected {
		LOG_WARNING(fmt.Sprintf("get state %+v", GetState(connId)))
		return -1
	}

	LOG_WARNING("login ok")
	return 0

}

func WmLogout(connId int) int {

	LOG_DEBUG("logout " + strconv.Itoa(connId))

	// sanity check arg
	if connId == -1 {
		LOG_WARNING("invalid connId")
		return -1
	}

	// get client
	var client *whatsmeow.Client = GetClient(connId)

	// disconnect
	client.Disconnect()

	// set state
	SetState(connId, Disconnected)

	LOG_DEBUG("logout ok")

	return 0
}

func WmCleanup(connId int) int {

	LOG_DEBUG("cleanup " + strconv.Itoa(connId))
	RemoveConn(connId)
	return 0
}

func WmGetMessages(connId int, chatId string, limit int, fromMsgId string, owner int) int {
	// not supported in multi-device
	return -1
}

func WmSendMessage(connId int, chatId string, text string, quotedId string, quotedText string, quotedSender string, filePath string, fileType string) int {

	LOG_TRACE("send message " + strconv.Itoa(connId) + ", " + chatId + ", " + text)

	// sanity check arg
	if connId == -1 {
	 	LOG_WARNING("invalid connId")
	 	return -1
	}

	// get conn
	var client *whatsmeow.Client = GetClient(connId)

	// local vars
	var sendErr error
	var message waProto.Message
	var msgId types.MessageID
	var timeStamp time.Time

	// recipient
	chatJid, jidErr := types.ParseJID(chatId)
	if jidErr != nil {
		LOG_WARNING(fmt.Sprintf("jid err"))
		return -1
	}

	// check message type
	if len(filePath) == 0 {

	 	// text message

	 	if len(quotedId) > 0 {
			contextInfo := waProto.ContextInfo{}
			quotedMessage := waProto.Message{
				Conversation: &quotedText,
			}

			selfId := JidToStr(*client.Store.ID)
	 		if quotedSender == selfId {
	 			quotedSender = chatId
	 		}

	 		quotedSender = strings.Replace(quotedSender, "@c.us", "@s.whatsapp.net", 1)

	 		LOG_TRACE("send quoted " + quotedId + ", " + quotedText + ", " + quotedSender)
	 		contextInfo = waProto.ContextInfo{
	 			QuotedMessage:   &quotedMessage,
	 			StanzaId: &quotedId,
	 			Participant:     &quotedSender,
	 		}

			extendedTextMessage := waProto.ExtendedTextMessage{
				Text:        &text,
				ContextInfo: &contextInfo,
			}

			message.ExtendedTextMessage = &extendedTextMessage
	 	} else {
			message.Conversation = &text
		}

		// message id
		msgId = whatsmeow.GenerateMessageID()
		
	 	// send message
	 	timeStamp, sendErr = client.SendMessage(chatJid, msgId, &message)

	} else {

		mimeType := strings.Split(fileType, "/")[0] // image, text, application, etc.
		if mimeType == "image" {

			LOG_TRACE("send image " + fileType)

			data, err := os.ReadFile(filePath)
			if err != nil {
				LOG_WARNING(fmt.Sprintf("rec err"))
				//log.Errorf("Failed to read %s: %v", args[0], err)
				return -1
			}
			
			uploaded, upErr := client.Upload(context.Background(), data, whatsmeow.MediaImage)
			if upErr != nil {
				LOG_WARNING(fmt.Sprintf("rec err"))
				//log.Errorf("Failed to upload file: %v", err)
				return -1
			}

			imageMessage := waProto.ImageMessage{
				Caption:       proto.String(text),
				Url:           proto.String(uploaded.URL),
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String(fileType),
				FileEncSha256: uploaded.FileEncSHA256,
				FileSha256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uint64(len(data))),
			}

			message.ImageMessage = &imageMessage

			// message id
			msgId = whatsmeow.GenerateMessageID()
		
			// send message
			timeStamp, sendErr = client.SendMessage(chatJid, msgId, &message)

	 	} else {

			LOG_TRACE("send document " + fileType)

			data, err := os.ReadFile(filePath)
			if err != nil {
				LOG_WARNING(fmt.Sprintf("rec err"))
				//log.Errorf("Failed to read %s: %v", args[0], err)
				return -1
			}
			
			uploaded, upErr := client.Upload(context.Background(), data, whatsmeow.MediaDocument)
			if upErr != nil {
				LOG_WARNING(fmt.Sprintf("rec err"))
				//log.Errorf("Failed to upload file: %v", err)
				return -1
			}

			fileName := filepath.Base(filePath)
			
			documentMessage := waProto.DocumentMessage{
				//Caption:       proto.String(text),
				Url:           proto.String(uploaded.URL),
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String(fileType),
				FileEncSha256: uploaded.FileEncSHA256,
				FileSha256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uint64(len(data))),
				FileName:      proto.String(fileName),
			}

			message.DocumentMessage = &documentMessage

			// message id
			msgId = whatsmeow.GenerateMessageID()
		
			// send message
			timeStamp, sendErr = client.SendMessage(chatJid, msgId, &message)

	 	}
	}

	// log any error
	if sendErr != nil {
	 	LOG_WARNING(fmt.Sprintf("send message error %+v", sendErr))
	 	return -1
	} else {
	 	LOG_TRACE(fmt.Sprintf("send message ok"))

		// messageInfo
		var messageInfo types.MessageInfo
		messageInfo.Chat = chatJid
		messageInfo.ID = msgId
		messageInfo.IsFromMe = true
		messageInfo.Sender = *client.Store.ID
		messageInfo.Timestamp = timeStamp

		handler := GetHandler(connId)
		handler.HandleMessage(messageInfo, &message, false)
	}

	return 0
}

func WmMarkMessageRead(connId int, chatId string, msgId string) int {

	LOG_TRACE("mark message read " + strconv.Itoa(connId) + ", " + chatId + ", " + msgId)

	// sanity check arg
	if connId == -1 {
	 	LOG_WARNING("invalid connId")
		return -1
	}

	// get client
	client := GetClient(connId)

	// mark read
	msgIds := []types.MessageID{
		msgId,
	}
	timeNow := time.Now()
	selfJid := *client.Store.ID
	chatJid, _ := types.ParseJID(chatId)
	err := client.MarkRead(msgIds, timeNow, chatJid, selfJid)

	// log any error
	if err != nil {
	 	LOG_WARNING(fmt.Sprintf("mark message read error %+v", err))
	 	return -1
	} else {
	 	LOG_TRACE(fmt.Sprintf("mark message read ok %+v", msgId))
	}

	return 0
}

func WmDeleteMessage(connId int, chatId string, msgId string) int {

	LOG_TRACE("delete message " + strconv.Itoa(connId) + ", " + chatId + ", " + msgId)

	// sanity check arg
	if connId == -1 {
	 	LOG_WARNING("invalid connId")
	 	return -1
	}

	// get client
	client := GetClient(connId)

	// delete message
	chatJid, _ := types.ParseJID(chatId)
	_, err := client.RevokeMessage(chatJid, msgId)

	// log any error
	if err != nil {
	 	LOG_WARNING(fmt.Sprintf("delete message error %+v", err))
	 	return -1
	} else {
	 	LOG_TRACE(fmt.Sprintf("delete message ok %+v", msgId))
	}

	return 0
}

func WmSendTyping(connId int, chatId string, isTyping int) int {

	LOG_TRACE("send typing " + strconv.Itoa(connId) + ", " + chatId + ", " + strconv.Itoa(isTyping))

	// sanity check arg
	if connId == -1 {
	 	LOG_WARNING("invalid connId")
	 	return -1
	}

	// get client
	client := GetClient(connId)

	// set presence
	var chatPresence types.ChatPresence = types.ChatPresencePaused
	if isTyping == 1 {
	 	chatPresence = types.ChatPresenceComposing
	}

	var chatPresenceMedia types.ChatPresenceMedia = types.ChatPresenceMediaText
	chatJid, _ := types.ParseJID(chatId)
	err := client.SendChatPresence(chatJid, chatPresence, chatPresenceMedia)

	// log any error
	if err != nil {
	 	LOG_WARNING(fmt.Sprintf("send typing error %+v", err))
	 	return -1
	} else {
	 	LOG_TRACE(fmt.Sprintf("send typing ok"))
	}

	return 0
}

func WmSetStatus(connId int, isOnline int) int {

	LOG_TRACE("set status " + strconv.Itoa(connId) + ", " + strconv.Itoa(isOnline))

	 // sanity check arg
	 if connId == -1 {
	 	LOG_WARNING("invalid connId")
	 	return -1
	 }

	// get client
	client := GetClient(connId)

	// bail out if no push name
	if len(client.Store.PushName) == 0 {
	 	LOG_WARNING("tmp")
		return -1
	}

	// set presence
	var presence types.Presence = types.PresenceUnavailable
	if isOnline == 1 {
	 	presence = types.PresenceAvailable
	}

	err := client.SendPresence(presence)
	if err != nil {
		LOG_WARNING("Failed to send presence")
	} else {
		LOG_TRACE("Sent presence ok")
	}

	return 0
}