package main

import "context"

var buildVersion = "dev"

type runtimeEngine interface {
	Start(context.Context) error
	Shutdown(context.Context) error
}
