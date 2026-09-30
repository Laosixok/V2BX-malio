package core

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
)

type AddUsersParams struct {
	Tag   string
	Users []panel.UserInfo
	*panel.NodeInfo
}

// UserTrafficCleaner is optional for cores that cache traffic by user. Callers
// must exclude users that are being re-added to update their limits.
type UserTrafficCleaner interface {
	DeleteUserTraffic(tag string, users []string)
}

type Core interface {
	Start() error
	Close() error
	AddNode(tag string, info *panel.NodeInfo, config *conf.Options) error
	DelNode(tag string) error
	AddUsers(p *AddUsersParams) (added int, err error)
	GetUserTraffic(tag, uuid string, reset bool) (up int64, down int64)
	DelUsers(users []panel.UserInfo, tag string, info *panel.NodeInfo) error
	Protocols() []string
	Type() string
}
