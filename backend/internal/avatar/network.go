package avatar

import (
	"context"
	"fmt"
	"net"
	"time"
)

func publicDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var addresses []net.IPAddr
	var lookupErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		addresses, lookupErr = net.DefaultResolver.LookupIPAddr(ctx, host)
		if lookupErr == nil && len(addresses) > 0 {
			break
		}
	}
	if lookupErr != nil || len(addresses) == 0 {
		if lookupErr == nil {
			return nil, fmt.Errorf("无法解析媒体地址")
		}
		return nil, lookupErr
	}
	public := make([]net.IPAddr, 0, len(addresses))
	for _, a := range addresses {
		if a.IP.IsPrivate() || a.IP.IsLoopback() || a.IP.IsUnspecified() || a.IP.IsLinkLocalUnicast() || a.IP.IsLinkLocalMulticast() || !a.IP.IsGlobalUnicast() {
			continue
		}
		public = append(public, a)
	}
	if len(public) == 0 {
		return nil, fmt.Errorf("不允许访问非公网媒体地址")
	}
	for _, a := range public {
		conn, e := (&net.Dialer{Timeout: 20 * time.Second}).DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if e == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("无法连接媒体服务器")
}
