package mocks

import (
	context "context"
	reflect "reflect"

	gomock "github.com/golang/mock/gomock"
)

// MockSSHRunner is a mock of SSHRunner interface.
type MockSSHRunner struct {
	ctrl     *gomock.Controller
	recorder *MockSSHRunnerMockRecorder
}

// MockSSHRunnerMockRecorder is the mock recorder for MockSSHRunner.
type MockSSHRunnerMockRecorder struct {
	mock *MockSSHRunner
}

// NewMockSSHRunner creates a new mock instance.
func NewMockSSHRunner(ctrl *gomock.Controller) *MockSSHRunner {
	mock := &MockSSHRunner{ctrl: ctrl}
	mock.recorder = &MockSSHRunnerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockSSHRunner) EXPECT() *MockSSHRunnerMockRecorder {
	return m.recorder
}

// RunCommand mocks base method.
func (m *MockSSHRunner) RunCommand(arg0 context.Context, arg1, arg2 string) (string, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RunCommand", arg0, arg1, arg2)
	ret0, _ := ret[0].(string)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// RunCommand indicates an expected call of RunCommand.
func (mr *MockSSHRunnerMockRecorder) RunCommand(arg0, arg1, arg2 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RunCommand", reflect.TypeOf((*MockSSHRunner)(nil).RunCommand), arg0, arg1, arg2)
}
