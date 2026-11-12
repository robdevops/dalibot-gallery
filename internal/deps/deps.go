// Package deps pins the module's dependencies in go.mod while the packages
// that use them are still being written. The server phase deletes this file.
package deps

import (
	_ "github.com/aws/aws-sdk-go-v2/config"
	_ "github.com/aws/aws-sdk-go-v2/credentials"
	_ "github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	_ "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	_ "github.com/davidbyttow/govips/v2/vips"
	_ "golang.org/x/sync/errgroup"
	_ "golang.org/x/sync/semaphore"
	_ "golang.org/x/sync/singleflight"
)
