package rod_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/defaults"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/rod/lib/utils"
	"github.com/tidwall/sjson"
)

func (s *S) TestIncognito() {
	file := srcFile("fixtures/click.html")
	k := utils.RandString(8)

	b := s.browser.MustIncognito()
	page := b.MustPage(file)
	defer page.MustClose()
	page.MustEval(`k => localStorage[k] = 1`, k)

	s.Nil(s.page.MustNavigate(file).MustEval(`k => localStorage[k]`, k).Value())
	s.EqualValues(1, page.MustEval(`k => localStorage[k]`, k).Int())
}

func (s *S) TestPageErr() {
	s.Panics(func() {
		s.mc.stubErr(1, proto.TargetAttachToTarget{})
		s.browser.MustPage("")
	})
}

func (s *S) TestPageFromTarget() {
	s.Panics(func() {
		res, err := proto.TargetCreateTarget{URL: "about:blank"}.Call(s.browser)
		utils.E(err)
		defer func() {
			s.browser.MustPageFromTargetID(res.TargetID).MustClose()
		}()

		s.mc.stubErr(1, proto.EmulationSetDeviceMetricsOverride{})
		s.browser.MustPageFromTargetID(res.TargetID)
	})
}

func (s *S) TestBrowserPages() {
	page := s.browser.MustPage(srcFile("fixtures/click.html")).MustWaitLoad()
	defer page.MustClose()

	pages := s.browser.MustPages()

	s.Len(pages, 2)

	{
		s.mc.stub(1, proto.TargetGetTargets{}, func(send func() ([]byte, error)) ([]byte, error) {
			d, _ := send()
			return sjson.SetBytes(d, "targetInfos.0.type", "iframe")
		})
		pages := s.browser.MustPages()
		s.Len(pages, 1)
	}

	s.Panics(func() {
		s.mc.stubErr(1, proto.TargetCreateTarget{})
		s.browser.MustPage("")
	})
	s.Panics(func() {
		s.mc.stubErr(1, proto.TargetGetTargets{})
		s.browser.MustPages()
	})
	s.Panics(func() {
		res, err := proto.TargetCreateTarget{URL: "about:blank"}.Call(s.browser)
		utils.E(err)
		defer func() {
			s.browser.MustPageFromTargetID(res.TargetID).MustClose()
		}()
		s.mc.stubErr(1, proto.TargetAttachToTarget{})
		s.browser.MustPages()
	})
}

func (s *S) TestBrowserClearStates() {
	utils.E(proto.EmulationClearGeolocationOverride{}.Call(s.page))

	defer s.browser.EnableDomain("", &proto.TargetSetDiscoverTargets{Discover: true})()
	s.browser.DisableDomain("", &proto.TargetSetDiscoverTargets{Discover: false})()
}

func (s *S) TestBrowserWaitEvent() {
	s.NotNil(s.browser.Event())

	wait := s.page.WaitEvent(&proto.PageFrameNavigated{})
	s.page.MustNavigate(srcFile("fixtures/click.html"))
	wait()
}

func (s *S) TestBrowserCrash() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	browser := rod.New().Context(ctx).MustConnect()
	page := browser.MustPage("")

	_ = proto.BrowserCrash{}.Call(browser)

	s.Panics(func() {
		page.MustEval(`new Promise(() => {})`)
	})
}

func (s *S) TestBrowserCall() {
	v, err := proto.BrowserGetVersion{}.Call(s.browser)
	utils.E(err)

	s.Regexp("1.3", v.ProtocolVersion)
}

func (s *S) TestMonitor() {
	b := rod.New().Timeout(1 * time.Minute).MustConnect()
	defer b.MustClose()
	p := b.MustPage(srcFile("fixtures/click.html")).MustWaitLoad()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := b.Context(ctx).ServeMonitor("")

	page := s.page.MustNavigate(host)
	s.Contains(page.MustElement("#targets a").MustParent().MustHTML(), string(p.TargetID))

	page.MustNavigate(host + "/page/" + string(p.TargetID))
	page.MustWait(`(id) => document.title.includes(id)`, p.TargetID)

	res, err := http.Get(host + "/screenshot")
	utils.E(err)
	s.Greater(len(utils.MustReadBytes(res.Body)), 10)

	res, err = http.Get(host + "/api/page/test")
	utils.E(err)
	s.Equal(400, res.StatusCode)
	s.EqualValues(-32602, utils.MustReadJSON(res.Body).Get("code").Int())
}

func (s *S) TestMonitorErr() {
	defaults.Monitor = "abc"
	defer defaults.ResetWithEnv()

	l := launcher.New()
	u := l.MustLaunch()
	defer func() {
		utils.Sleep(1) // if kill too fast, the parent process of the browser may not be ready
		l.Kill()
	}()

	s.Panics(func() {
		rod.New().ControlURL(u).MustConnect()
	})
}

func (s *S) TestRemoteLaunch() {
	url, mux, close := utils.Serve("")
	defer close()

	defaults.Remote = true
	defaults.URL = url
	defer defaults.ResetWithEnv()

	proxy := launcher.NewProxy()
	mux.Handle("/", proxy)

	b := rod.New().MustConnect()
	defer b.MustClose()

	p := b.MustPage(srcFile("fixtures/click.html"))
	p.MustElement("button").MustClick()
	s.True(p.MustHas("[a=ok]"))
}

func (s *S) TestTrace() {
	var msg *rod.TraceMsg
	s.browser.TraceLog(func(m *rod.TraceMsg) { msg = m })
	s.browser.Trace(true).Slowmotion(time.Microsecond)
	defer func() {
		s.browser.TraceLog(nil)
		s.browser.Trace(defaults.Trace).Slowmotion(defaults.Slow)
	}()

	p := s.page.MustNavigate(srcFile("fixtures/click.html"))
	el := p.MustElement("button")
	el.MustClick()

	s.Equal(rod.TraceTypeInput, msg.Type)
	s.Equal("left click", msg.Details)
	s.Equal(`[input] "left click"`, msg.String())

	s.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
	_ = p.Mouse.Move(10, 10, 1)
}

func (s *S) TestTraceLogs() {
	s.browser.Trace(true)
	defer func() {
		s.browser.Trace(defaults.Trace)
	}()

	p := s.page.MustNavigate(srcFile("fixtures/click.html"))
	el := p.MustElement("button")
	el.MustClick()

	s.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
	p.Overlay(0, 0, 100, 30, "")
}

func (s *S) TestConcurrentOperations() {
	p := s.page.MustNavigate(srcFile("fixtures/click.html"))
	list := []int64{}
	lock := sync.Mutex{}
	add := func(item int64) {
		lock.Lock()
		defer lock.Unlock()
		list = append(list, item)
	}

	utils.All(func() {
		add(p.MustEval(`new Promise(r => setTimeout(r, 100, 2))`).Int())
	}, func() {
		add(p.MustEval(`1`).Int())
	})()

	s.Equal([]int64{1, 2}, list)
}

func (s *S) TestPromiseLeak() {
	/*
		Perform a slow action then navigate the page to another url,
		we can see the slow operation will still be executed.

		The unexpected part is that the promise will resolve to the next page's url.
	*/

	p := s.page.MustNavigate(srcFile("fixtures/click.html"))
	var out string

	utils.All(func() {
		out = p.MustEval(`new Promise(r => setTimeout(() => r(location.href), 200))`).String()
	}, func() {
		utils.Sleep(0.1)
		p.MustNavigate(srcFile("fixtures/input.html"))
	})()

	s.Contains(out, "input.html")
}

func (s *S) TestObjectLeak() {
	/*
		Seems like it won't leak
	*/

	p := s.page.MustNavigate(srcFile("fixtures/click.html"))

	el := p.MustElement("button")
	p.MustNavigate(srcFile("fixtures/input.html")).MustWaitLoad()
	s.Panics(func() {
		el.MustDescribe()
	})
}

func (s *S) TestBlockingNavigation() {
	/*
		Navigate can take forever if a page doesn't response.
		If one page is blocked, other pages should still work.
	*/

	url, mux, close := utils.Serve("")
	defer close()
	pause, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		<-pause.Done()
	})
	mux.HandleFunc("/b", httpHTML(`<html>ok</html>`))

	blocked := s.browser.MustPage("")
	defer blocked.MustClose()

	go func() {
		s.Panics(func() {
			blocked.MustNavigate(url + "/a")
		})
	}()

	utils.Sleep(0.3)

	p := s.browser.MustPage(url + "/b")
	defer p.MustClose()
}

func (s *S) TestResolveBlocking() {
	url, mux, close := utils.Serve("")
	defer close()

	pause, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		<-pause.Done()
	})

	p := s.browser.MustPage("")
	defer p.MustClose()

	go func() {
		utils.Sleep(0.1)
		p.MustStopLoading()
	}()

	s.Panics(func() {
		p.MustNavigate(url)
	})
}

func (s *S) TestTry() {
	s.Nil(rod.Try(func() {}))

	err := rod.Try(func() { panic(1) })
	var errVal *rod.Error
	ok := errors.As(err, &errVal)
	s.True(ok)
	s.Equal(1, errVal.Details)
}

func (s *S) TestBrowserOthers() {
	s.browser.Timeout(time.Hour).CancelTimeout().MustPages()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s.Panics(func() {
		s.browser.Context(ctx).MustIncognito()
	})
}

func (s *S) TestBinarySize() {
	if runtime.GOOS == "windows" {
		s.T().SkipNow()
	}

	cmd := exec.Command("go", "build",
		"-trimpath",
		"-ldflags", "-w -s",
		"-o", "tmp/translator",
		"./lib/examples/translator")

	cmd.Env = append(os.Environ(), "GOOS=linux")

	out, err := cmd.CombinedOutput()
	if err != nil {
		s.T().Skip(err, string(out))
	}

	stat, err := os.Stat("tmp/translator")
	utils.E(err)

	s.Less(float64(stat.Size())/1024/1024, 8.65) // mb
}

func (s *S) TestBrowserConnectErr() {
	s.Panics(func() {
		ctx, cancel := context.WithCancel(context.Background())
		defaults.Remote = true
		defer defaults.ResetWithEnv()

		cancel()
		rod.New().Context(ctx).MustConnect()
	})

	s.Panics(func() {
		c := newMockClient(s, nil)
		c.connect = func() error { return errors.New("err") }
		rod.New().Client(c).MustConnect()
	})

	s.Panics(func() {
		ch := make(chan *cdp.Event)
		defer close(ch)

		c := newMockClient(s, nil)
		c.connect = func() error { return nil }
		c.event = ch
		c.stubErr(1, proto.BrowserGetBrowserCommandLine{})
		rod.New().Client(c).MustConnect()
	})
}

func (s *S) TestStreamReader() {
	r := rod.NewStreamReader(s.page, "")

	s.mc.stub(1, proto.IORead{}, func(send func() ([]byte, error)) ([]byte, error) {
		return utils.MustToJSONBytes(proto.IOReadResult{
			Data: "test",
		}), nil
	})
	b := make([]byte, 4)
	_, _ = r.Read(b)
	s.Equal("test", string(b))

	s.mc.stubErr(1, proto.IORead{})
	_, err := r.Read(nil)
	s.Error(err)

	s.mc.stub(1, proto.IORead{}, func(send func() ([]byte, error)) ([]byte, error) {
		return utils.MustToJSONBytes(proto.IOReadResult{
			Base64Encoded: true,
			Data:          "@",
		}), nil
	})
	_, err = r.Read(nil)
	s.Error(err)
}

// It's obvious that, the v8 will take more time to parse long function.
// For BenchmarkCache and BenchmarkNoCache, the difference is nearly 12% which is too much to ignore.
func BenchmarkCacheOff(b *testing.B) {
	p := rod.New().Timeout(1 * time.Minute).MustConnect().MustPage(srcFile("fixtures/click.html"))

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		p.MustEval(`(time) => {
			// won't call this function, it's used to make the declaration longer
			function foo (id, left, top, width, height, msg) {
				var div = document.createElement('div')
				var msgDiv = document.createElement('div')
				div.id = id
				div.style = 'position: fixed; z-index:2147483647; border: 2px dashed red;'
					+ 'border-radius: 3px; box-shadow: #5f3232 0 0 3px; pointer-events: none;'
					+ 'box-sizing: border-box;'
					+ 'left:' + left + 'px;'
					+ 'top:' + top + 'px;'
					+ 'height:' + height + 'px;'
					+ 'width:' + width + 'px;'
		
				if (height === 0) {
					div.style.border = 'none'
				}
			
				msgDiv.style = 'position: absolute; color: #cc26d6; font-size: 12px; background: #ffffffeb;'
					+ 'box-shadow: #333 0 0 3px; padding: 2px 5px; border-radius: 3px; white-space: nowrap;'
					+ 'top:' + height + 'px; '
			
				msgDiv.innerHTML = msg
			
				div.appendChild(msgDiv)
				document.body.appendChild(div)
			}
			return time
		}`, time.Now().UnixNano())
	}
}

func BenchmarkCache(b *testing.B) {
	p := rod.New().Timeout(1 * time.Minute).MustConnect().MustPage(srcFile("fixtures/click.html"))

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		p.MustEval(`(time) => {
			return time
		}`, time.Now().UnixNano())
	}
}
