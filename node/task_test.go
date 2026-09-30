package node

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/task"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
)

type monitorTestCore struct {
	vCore.Core
	traffic     *counter.TrafficCounter
	deleted     []panel.UserInfo
	added       []panel.UserInfo
	deleteError error
	deletedTag  string
	addedTag    string
}

func (c *monitorTestCore) DelUsers(users []panel.UserInfo, _ string, _ *panel.NodeInfo) error {
	if c.deleteError != nil {
		return c.deleteError
	}
	c.deleted = append(c.deleted, users...)
	return nil
}

func (c *monitorTestCore) AddUsers(params *vCore.AddUsersParams) (int, error) {
	c.added = append(c.added, params.Users...)
	return len(params.Users), nil
}

func (c *monitorTestCore) DeleteUserTraffic(_ string, users []string) {
	for _, user := range users {
		c.traffic.Delete(user)
	}
}

func (c *monitorTestCore) DelNode(tag string) error {
	c.deletedTag = tag
	return nil
}

func (c *monitorTestCore) AddNode(tag string, _ *panel.NodeInfo, _ *conf.Options) error {
	c.addedTag = tag
	return nil
}

func newMonitorTestController(t *testing.T, host string, users []panel.UserInfo) (*Controller, *monitorTestCore) {
	t.Helper()
	api, err := panel.New(&conf.ApiConfig{APIHost: host, NodeType: "vless", NodeID: 1})
	if err != nil {
		t.Fatal(err)
	}
	server := &monitorTestCore{traffic: counter.NewTrafficCounter()}
	c := NewController(server, api, &conf.Options{})
	c.tag = "old-tag"
	c.userList = users
	c.info = &panel.NodeInfo{Type: "vless"}
	limiter.Init()
	c.limiter = limiter.AddLimiter(c.tag, &c.LimitConfig, users, map[int]int{})
	return c, server
}

func TestMonitorUserTrafficCleanup(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        int
		body          string
		deleteError   error
		wantUsers     int
		wantDeleted   int
		wantAdded     int
		wantCounters  int
		wantRemaining int64
	}{
		{
			name: "limit change retains traffic and removed user is cleared", status: http.StatusOK,
			body:      `{"ret":1,"data":[{"id":1,"uuid":"remaining","node_speedlimit":2}]}`,
			wantUsers: 1, wantDeleted: 2, wantAdded: 1, wantCounters: 1, wantRemaining: 7,
		},
		{
			name: "empty SSPanel list removes every user", status: http.StatusOK,
			body: `{"ret":1,"data":[]}`, wantDeleted: 2,
		},
		{
			name: "SSPanel 304 retains all users", status: http.StatusNotModified,
			wantUsers: 2, wantCounters: 2, wantRemaining: 7,
		},
		{
			name: "failed deletion preserves traffic", status: http.StatusOK,
			body: `{"ret":1,"data":[]}`, deleteError: errors.New("delete failed"),
			wantUsers: 2, wantCounters: 2, wantRemaining: 7,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path == "/mod_mu/nodes/1/info" {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				if r.URL.Path != "/mod_mu/users" {
					t.Errorf("unexpected API request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer api.Close()
			c, server := newMonitorTestController(t, api.URL, []panel.UserInfo{
				{Id: 1, Uuid: "remaining", SpeedLimit: 1}, {Id: 2, Uuid: "removed"},
			})
			server.traffic.Tx("remaining", 7)
			server.traffic.Tx("removed", 9)
			server.deleteError = test.deleteError
			if err := c.nodeInfoMonitor(); err != nil {
				t.Fatal(err)
			}
			if len(c.userList) != test.wantUsers || len(server.deleted) != test.wantDeleted || len(server.added) != test.wantAdded {
				t.Fatalf("wrong user update: remaining=%d deleted=%d added=%d", len(c.userList), len(server.deleted), len(server.added))
			}
			if server.traffic.Len() != test.wantCounters || server.traffic.GetUpCount("remaining") != test.wantRemaining {
				t.Fatalf("wrong traffic cleanup: counters=%d remaining=%d", server.traffic.Len(), server.traffic.GetUpCount("remaining"))
			}
			if got := requests.Load(); got != 2 {
				t.Fatalf("cleanup added API requests: got %d, want 2", got)
			}
		})
	}
}

func TestMonitorReloadReplacesOldLimiter(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/mod_mu/nodes/1/info" {
			fmt.Fprint(w, `{"ret":1,"data":{"sort":16,"server":"example.test;port=443&security=reality"}}`)
		} else {
			w.WriteHeader(http.StatusNotModified)
		}
	}))
	defer api.Close()
	c, server := newMonitorTestController(t, api.URL, []panel.UserInfo{{Id: 1, Uuid: "remaining"}})
	c.nodeInfoMonitorPeriodic = &task.Task{Interval: time.Hour, Execute: c.nodeInfoMonitor}
	c.userReportPeriodic = &task.Task{Interval: time.Hour, Execute: func() error { return nil }}
	finished := make(chan error, 1)
	go func() { finished <- c.nodeInfoMonitorPeriodic.Start(true) }()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("node reload deadlocked while changing its own task interval")
	}
	defer c.nodeInfoMonitorPeriodic.Close()
	defer c.userReportPeriodic.Close()
	if _, err := limiter.GetLimiter("old-tag"); err == nil {
		t.Fatal("node reload retained the old limiter")
	}
	if _, err := limiter.GetLimiter(c.tag); err != nil {
		t.Fatal("node reload did not register its new limiter")
	}
	if server.deletedTag != "old-tag" || server.addedTag != c.tag || c.tag == "old-tag" {
		t.Fatalf("wrong node replacement: deleted=%q added=%q current=%q", server.deletedTag, server.addedTag, c.tag)
	}
	if c.nodeInfoMonitorPeriodic.Interval != time.Minute || c.userReportPeriodic.Interval != time.Minute {
		t.Fatal("node reload did not apply the panel's intervals")
	}
}

func TestMonitorUsesDistinctPushAndPullIntervals(t *testing.T) {
	c := &Controller{
		nodeInfoMonitorPeriodic: &task.Task{Interval: time.Minute},
		userReportPeriodic:      &task.Task{Interval: time.Minute, Execute: func() error { return nil }},
	}
	c.updateTaskIntervals(&panel.NodeInfo{PullInterval: time.Hour, PushInterval: 2 * time.Hour})
	defer c.userReportPeriodic.Close()
	if c.nodeInfoMonitorPeriodic.Interval != time.Hour || c.userReportPeriodic.Interval != 2*time.Hour {
		t.Fatal("push and pull intervals were not independently applied")
	}
}
