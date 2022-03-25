package aghnet

import (
	"io/fs"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/AdguardTeam/AdGuardHome/internal/aghtest"
	"github.com/AdguardTeam/golibs/errors"
	"github.com/AdguardTeam/golibs/netutil"
	"github.com/AdguardTeam/golibs/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	aghtest.DiscardLogOutput(m)
}

// testdata is the filesystem containing data for testing the package.
var testdata fs.FS = os.DirFS("./testdata")

func setTestRootDirFS(t testing.TB, fsys fs.FS) {
	prev := rootDirFS
	t.Cleanup(func() {
		rootDirFS = prev
	})
	rootDirFS = fsys
}

// testShell is a substitution of aghos.RunCommand that maps the command to it's
// execution result.  It's only needed to simplify testing.
//
// TODO(e.burkov):  Perhaps put all the shell interactions behind an interface.
type testShell map[string]struct {
	err  error
	out  string
	code int
}

func (rc testShell) set(t testing.TB) {
	t.Helper()

	prev := aghosRunCommand
	t.Cleanup(func() { aghosRunCommand = prev })
	aghosRunCommand = func(cmd string, args ...string) (code int, output []byte, err error) {
		key := strings.Join(append([]string{cmd}, args...), " ")
		ret := rc[key]

		return ret.code, []byte(ret.out), ret.err
	}
}

func TestGatewayIP(t *testing.T) {
	testCases := []struct {
		name string
		rcs  testShell
		want net.IP
	}{{
		name: "success_v4",
		rcs: testShell{"ip route show dev ifaceName": {
			err:  nil,
			out:  `default via 1.2.3.4 onlink`,
			code: 0,
		}},
		want: net.IP{1, 2, 3, 4}.To16(),
	}, {
		name: "success_v6",
		rcs: testShell{"ip route show dev ifaceName": {
			err:  nil,
			out:  `default via ::ffff onlink`,
			code: 0,
		}},
		want: net.IP{
			0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0,
			0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0xFF, 0xFF,
		},
	}, {
		name: "bad_output",
		rcs: testShell{"ip route show dev ifaceName": {
			err:  nil,
			out:  `non-default via 1.2.3.4 onlink`,
			code: 0,
		}},
		want: nil,
	}, {
		name: "err_runcmd",
		rcs: testShell{"ip route show dev ifaceName": {
			err:  errors.Error("can't run command"),
			out:  ``,
			code: 0,
		}},
		want: nil,
	}, {
		name: "bad_code",
		rcs: testShell{"ip route show dev ifaceName": {
			err:  nil,
			out:  ``,
			code: 1,
		}},
		want: nil,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.rcs.set(t)

			assert.Equal(t, tc.want, GatewayIP("ifaceName"))
		})
	}
}

func TestGetInterfaceByIP(t *testing.T) {
	ifaces, err := GetValidNetInterfacesForWeb()
	require.NoError(t, err)
	require.NotEmpty(t, ifaces)

	for _, iface := range ifaces {
		t.Run(iface.Name, func(t *testing.T) {
			require.NotEmpty(t, iface.Addresses)

			for _, ip := range iface.Addresses {
				ifaceName := GetInterfaceByIP(ip)
				require.Equal(t, iface.Name, ifaceName)
			}
		})
	}
}

func TestBroadcastFromIPNet(t *testing.T) {
	known6 := net.IP{
		1, 2, 3, 4,
		5, 6, 7, 8,
		9, 10, 11, 12,
		13, 14, 15, 16,
	}

	testCases := []struct {
		name   string
		subnet *net.IPNet
		want   net.IP
	}{{
		name: "full",
		subnet: &net.IPNet{
			IP:   net.IP{192, 168, 0, 1},
			Mask: net.IPMask{255, 255, 15, 0},
		},
		want: net.IP{192, 168, 240, 255},
	}, {
		name: "ipv6_no_mask",
		subnet: &net.IPNet{
			IP: known6,
		},
		want: known6,
	}, {
		name: "ipv4_no_mask",
		subnet: &net.IPNet{
			IP: net.IP{192, 168, 1, 2},
		},
		want: net.IP{192, 168, 1, 255},
	}, {
		name: "unspecified",
		subnet: &net.IPNet{
			IP:   net.IP{0, 0, 0, 0},
			Mask: net.IPMask{0, 0, 0, 0},
		},
		want: net.IPv4bcast,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bc := BroadcastFromIPNet(tc.subnet)
			assert.True(t, bc.Equal(tc.want), bc)
		})
	}
}

func TestCheckPort(t *testing.T) {
	t.Run("tcp_bound", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:")
		require.NoError(t, err)
		testutil.CleanupAndRequireSuccess(t, l.Close)

		ipp := netutil.IPPortFromAddr(l.Addr())
		require.NotNil(t, ipp)
		require.NotNil(t, ipp.IP)
		require.NotZero(t, ipp.Port)

		err = CheckPort("tcp", ipp.IP, ipp.Port)
		target := &net.OpError{}
		require.ErrorAs(t, err, &target)

		assert.Equal(t, "listen", target.Op)
	})

	t.Run("udp_bound", func(t *testing.T) {
		conn, err := net.ListenPacket("udp", "127.0.0.1:")
		require.NoError(t, err)
		testutil.CleanupAndRequireSuccess(t, conn.Close)

		ipp := netutil.IPPortFromAddr(conn.LocalAddr())
		require.NotNil(t, ipp)
		require.NotNil(t, ipp.IP)
		require.NotZero(t, ipp.Port)

		err = CheckPort("udp", ipp.IP, ipp.Port)
		target := &net.OpError{}
		require.ErrorAs(t, err, &target)

		assert.Equal(t, "listen", target.Op)
	})

	t.Run("bad_network", func(t *testing.T) {
		err := CheckPort("bad_network", nil, 0)
		assert.NoError(t, err)
	})

	t.Run("can_bind", func(t *testing.T) {
		err := CheckPort("udp", net.IP{0, 0, 0, 0}, 0)
		assert.NoError(t, err)
	})
}

func TestCollectAllIfacesAddrs(t *testing.T) {
	addrs, err := CollectAllIfacesAddrs()
	require.NoError(t, err)

	assert.NotEmpty(t, addrs)
}

func TestIsAddrInUse(t *testing.T) {
	t.Run("addr_in_use", func(t *testing.T) {
		l, err := net.Listen("tcp", "0.0.0.0:0")
		require.NoError(t, err)
		testutil.CleanupAndRequireSuccess(t, l.Close)

		_, err = net.Listen(l.Addr().Network(), l.Addr().String())
		assert.True(t, IsAddrInUse(err))
	})

	t.Run("another", func(t *testing.T) {
		const anotherErr errors.Error = "not addr in use"

		assert.False(t, IsAddrInUse(anotherErr))
	})
}
