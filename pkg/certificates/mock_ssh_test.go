package certificates

import (
	"io"
	"net"

	"github.com/golang/mock/gomock"
	"golang.org/x/crypto/ssh"
)

// Mock Client
type MockClient struct {
	ctrl     *gomock.Controller
	recorder *MockClientRecorder
}

type MockClientRecorder struct {
	mock *MockClient
}

func NewMockClient(ctrl *gomock.Controller) *MockClient {
	mock := &MockClient{ctrl: ctrl}
	mock.recorder = &MockClientRecorder{mock}
	return mock
}

func (m *MockClient) EXPECT() *MockClientRecorder {
	return m.recorder
}

func (m *MockClient) Close() error {
	ret := m.ctrl.Call(m, "Close")
	ret0, _ := ret[0].(error)
	return ret0
}

func (mr *MockClientRecorder) Close() *gomock.Call {
	return mr.mock.ctrl.RecordCall(mr.mock, "Close")
}

func (m *MockClient) NewSession() (*ssh.Session, error) {
	ret := m.ctrl.Call(m, "NewSession")
	if ret[0] == nil {
		return nil, ret[1].(error)
	}
	return ret[0].(*ssh.Session), ret[1].(error)
}

func (mr *MockClientRecorder) NewSession() *gomock.Call {
	return mr.mock.ctrl.RecordCall(mr.mock, "NewSession")
}

// Required by ssh.Client interface
func (m *MockClient) Dial(n, addr string) (net.Conn, error)              { return nil, nil }
func (m *MockClient) Listen(n, addr string) (net.Listener, error)        { return nil, nil }
func (m *MockClient) ListenTCP(laddr *net.TCPAddr) (net.Listener, error) { return nil, nil }
func (m *MockClient) ListenUnix(addr string) (net.Listener, error)       { return nil, nil }

// MockSession implements ssh.Session interface for testing
type MockSession struct {
	ctrl     *gomock.Controller
	recorder *MockSessionRecorder
}

type MockSessionRecorder struct {
	mock *MockSession
}

func NewMockSession(ctrl *gomock.Controller) *MockSession {
	mock := &MockSession{ctrl: ctrl}
	mock.recorder = &MockSessionRecorder{mock}
	return mock
}

func (m *MockSession) EXPECT() *MockSessionRecorder {
	return m.recorder
}

func (m *MockSession) Close() error {
	ret := m.ctrl.Call(m, "Close")
	ret0, _ := ret[0].(error)
	return ret0
}

func (mr *MockSessionRecorder) Close() *gomock.Call {
	return mr.mock.ctrl.RecordCall(mr.mock, "Close")
}

func (m *MockSession) Run(cmd string) error {
	ret := m.ctrl.Call(m, "Run", cmd)
	ret0, _ := ret[0].(error)
	return ret0
}

func (mr *MockSessionRecorder) Run(cmd interface{}) *gomock.Call {
	return mr.mock.ctrl.RecordCall(mr.mock, "Run", cmd)
}

func (m *MockSession) RequestPty(term string, h, w uint, modes ssh.TerminalModes) error {
	return nil
}
func (m *MockSession) Shell() error                       { return nil }
func (m *MockSession) Signal(sig ssh.Signal) error        { return nil }
func (m *MockSession) Start(cmd string) error             { return nil }
func (m *MockSession) StderrPipe() (io.Reader, error)     { return nil, nil }
func (m *MockSession) StdinPipe() (io.WriteCloser, error) { return nil, nil }
func (m *MockSession) StdoutPipe() (io.Reader, error)     { return nil, nil }
func (m *MockSession) Wait() error                        { return nil }
func (m *MockSession) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}
func (m *MockSession) Stderr() io.Writer { return nil }
func (m *MockSession) Stdin() io.Writer  { return nil }
func (m *MockSession) Stdout() io.Writer { return nil }
