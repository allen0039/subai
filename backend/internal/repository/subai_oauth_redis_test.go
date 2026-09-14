package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
)

type subaiRedisOAuthClient struct{ calls atomic.Int32 }

func (c *subaiRedisOAuthClient) ExchangeCode(context.Context, string, string, string, string, string) (*openai.TokenResponse, error) {
	c.calls.Add(1)
	return &openai.TokenResponse{AccessToken: "test", ExpiresIn: 3600}, nil
}
func (c *subaiRedisOAuthClient) RefreshToken(context.Context, string, string) (*openai.TokenResponse, error) {
	panic("unexpected refresh")
}
func (c *subaiRedisOAuthClient) RefreshTokenWithClientID(context.Context, string, string, string) (*openai.TokenResponse, error) {
	panic("unexpected refresh")
}
func TestSubAIOAuth_RedisRestartAndConcurrentExchange(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	defer func() { _ = rdb.Close() }()
	client := &subaiRedisOAuthClient{}
	first := service.NewOpenAIOAuthService(nil, client).WithSessionStore(openai.NewRedisSessionStore(rdb))
	result, err := first.GenerateAuthURL(context.Background(), nil, "", service.PlatformOpenAI, 7)
	require.NoError(t, err)
	parsed, _ := url.Parse(result.AuthURL)
	callback := openai.DefaultRedirectURI + "?code=c&state=" + parsed.Query().Get("state")
	first.Stop()
	a := service.NewOpenAIOAuthService(nil, client).WithSessionStore(openai.NewRedisSessionStore(rdb))
	defer a.Stop()
	b := service.NewOpenAIOAuthService(nil, client).WithSessionStore(openai.NewRedisSessionStore(rdb))
	defer b.Stop()
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc := a
			if i%2 == 0 {
				svc = b
			}
			_, err := svc.ExchangeCode(context.Background(), &service.OpenAIExchangeCodeInput{SessionID: result.SessionID, CallbackURL: callback, OwnerID: 7})
			if err == nil {
				successes.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, int32(1), client.calls.Load())
	require.Equal(t, int32(1), successes.Load())
	mr.Close()
	_, err = a.GenerateAuthURL(context.Background(), nil, "", service.PlatformOpenAI, 7)
	require.Error(t, err, "Redis failure must not create a local-only authorization session")
}
