package telegram

import (
	"fmt"
	"strconv"
)

type Identity struct {
	UserID   int64
	ChatID   int64
	ThreadID int64
	ChatType string
}

func (i Identity) SessionKey() string {
	if i.ChatType == "private" {
		return fmt.Sprintf("telegram:user:%d", i.UserID)
	}
	if i.ThreadID != 0 {
		return fmt.Sprintf("telegram:chat:%d:thread:%d:user:%d", i.ChatID, i.ThreadID, i.UserID)
	}
	return fmt.Sprintf("telegram:chat:%d:user:%d", i.ChatID, i.UserID)
}

func (i Identity) Metadata(updateID, messageID int64) map[string]string {
	return map[string]string{
		"telegram.update_id":  strconv.FormatInt(updateID, 10),
		"telegram.chat_id":    strconv.FormatInt(i.ChatID, 10),
		"telegram.message_id": strconv.FormatInt(messageID, 10),
		"telegram.thread_id":  strconv.FormatInt(i.ThreadID, 10),
	}
}
