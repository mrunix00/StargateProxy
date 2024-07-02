FROM golang:1.22.4-alpine3.20

WORKDIR /app

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY *.go ./

RUN go build -o /app/stargate_proxy

EXPOSE 8080

CMD ["/app/stargate_proxy"]