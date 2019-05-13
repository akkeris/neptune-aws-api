FROM golang:1.8-alpine

FROM golang:1.8-alpine as builder

RUN apk update && \
    apk add git --no-cache && \
    mkdir /go/src/neptune-aws-api

WORKDIR /go/src
RUN go get "github.com/go-martini/martini" && \
    go get "github.com/martini-contrib/render" && \
    go get "github.com/martini-contrib/binding"

RUN go get "github.com/aws/aws-sdk-go/aws" && \
    go get "github.com/aws/aws-sdk-go/aws/session" && \
    go get "github.com/aws/aws-sdk-go/service/iam" && \
    go get "github.com/aws/aws-sdk-go/service/neptune"

RUN go get "github.com/robfig/cron" && \
    go get "github.com/nu7hatch/gouuid" && \
    go get "github.com/lib/pq"

ADD neptune.go /go/src/neptune-aws-api
ADD api/api.go /go/src/neptune-aws-api/api/api.go
ADD preprovision/preprovision.go /go/src/neptune-aws-api/preprovision/preprovision.go

WORKDIR /go/src/neptune-aws-api
RUN go build neptune.go && \
    chmod +x /go/src/neptune-aws-api/neptune

FROM alpine:latest
COPY --from=builder /go/src/neptune-aws-api/neptune /
RUN apk add --no-cache ca-certificates && \
    apk add --no-cache tzdata

ENTRYPOINT ["/neptune"]
EXPOSE 3000