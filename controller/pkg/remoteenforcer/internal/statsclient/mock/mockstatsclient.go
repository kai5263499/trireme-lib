// Code generated by MockGen. DO NOT EDIT.
// Source: controller/pkg/remoteenforcer/internal/statsclient/interfaces.go

// Package mockstatsclient is a generated GoMock package.
package mockstatsclient

import (
	context "context"
	reflect "reflect"

	gomock "github.com/golang/mock/gomock"
)

// MockStatsClient is a mock of StatsClient interface
// nolint
type MockStatsClient struct {
	ctrl     *gomock.Controller
	recorder *MockStatsClientMockRecorder
}

// MockStatsClientMockRecorder is the mock recorder for MockStatsClient
// nolint
type MockStatsClientMockRecorder struct {
	mock *MockStatsClient
}

// NewMockStatsClient creates a new mock instance
// nolint
func NewMockStatsClient(ctrl *gomock.Controller) *MockStatsClient {
	mock := &MockStatsClient{ctrl: ctrl}
	mock.recorder = &MockStatsClientMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use
// nolint
func (m *MockStatsClient) EXPECT() *MockStatsClientMockRecorder {
	return m.recorder
}

// Run mocks base method
// nolint
func (m *MockStatsClient) Run(ctx context.Context) error {
	ret := m.ctrl.Call(m, "Run", ctx)
	ret0, _ := ret[0].(error)
	return ret0
}

// Run indicates an expected call of Run
// nolint
func (mr *MockStatsClientMockRecorder) Run(ctx interface{}) *gomock.Call {
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Run", reflect.TypeOf((*MockStatsClient)(nil).Run), ctx)
}
