FROM golang:1.19.7

WORKDIR /app/go-source-server
COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./

RUN CGO_ENABLED=0 go build .

RUN mkdir -p /data
RUN chown nobody:nogroup /data

USER nobody
EXPOSE 8090

ENTRYPOINT ["/app/go-source-server/go-source-server", "-data-dir=/data"]
