package telegram

type Update struct {
	ID      int64
	Message *Message
}

type Message struct {
	ID       int64
	From     *User
	Chat     Chat
	Text     string
	ThreadID int64
}

type User struct {
	ID       int64
	IsBot    bool
	Username string
}

type Chat struct {
	ID   int64
	Type string
}

type Target struct {
	ChatID   int64
	ThreadID int64
	ReplyTo  int64
}

type SentMessage struct {
	ID     int64
	ChatID int64
	Text   string
}
