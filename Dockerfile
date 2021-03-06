FROM golang:1.18

WORKDIR /work
COPY . ./
RUN go mod tidy
RUN go build -o ribapuro

FROM golang:1.18

RUN useradd app
USER app
WORKDIR /app
COPY --from=0 /work/ribapuro /app/ribapuro
VOLUME /app/sites

EXPOSE 8080
CMD ["/app/ribapuro"]
