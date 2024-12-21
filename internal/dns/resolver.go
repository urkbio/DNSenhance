package dnspkg

import (
	"net/http"
	"time"

	"github.com/miekg/dns"
)

type Resolver interface {
	Resolve(request *dns.Msg) (*dns.Msg, error)
}

type DOHResolver struct {
	endpoint  string
	client    *http.Client
	failCount int32
	lastFail  time.Time
}
