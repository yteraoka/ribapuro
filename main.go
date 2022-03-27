package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	//"net/url"
	"os"
	"path/filepath"
)

func main() {
	// https://qiita.com/izumin5210/items/7cdefe52cc54794c85fc
	dialerFunc := func(ctx context.Context, network, address string) (net.Conn, error) {
		d := net.Dialer{}
		return d.DialContext(ctx, "udp", "1.1.1.1:53")
	}
	resolver := &net.Resolver{PreferGo: true, Dial: dialerFunc}
	dialer := net.Dialer{Resolver: resolver}
	transport := &http.Transport{
		Dial: dialer.Dial,
		DialContext: dialer.DialContext,
	}

	director := func(req *http.Request) {
		req.URL.Host = req.Host
		if req.Header.Get("x-forwarded-proto") == "https" {
			req.URL.Scheme = "https"
		} else {
			req.URL.Scheme = "http"
		}
		log.Printf("%s://%s%s\n", req.URL.Scheme, req.URL.Host, req.URL.Path)
	}

	modifyFunc := func(resp *http.Response) error {
		//u, err := url.Parse(resp.Request.URL)
		//if err != nil {
		//	return err
		//}
		//path := filepath.Join(".", u.Path)
		localFile := filepath.Join("sites", resp.Request.URL.Host, resp.Request.URL.Path)
		if resp.Request.URL.Path == "/" {
			localFile = filepath.Join(localFile, "index.html")
		}
		fmt.Printf("path: %s\n", localFile)
		//fmt.Printf("%+v\n", resp)
		//fmt.Printf("%+v\n", resp.Request)
		//fmt.Println(resp.Header.Get("content-type"))
		buf, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		resp.Body = io.NopCloser(bytes.NewReader(buf))
		err = os.MkdirAll(filepath.Dir(localFile), 0750)
		if err == nil || os.IsExist(err) {
			err = os.WriteFile(localFile, buf, 0660)
			if err != nil {
				log.Println(err)
			}
		}
		return nil
	}
	proxy := &httputil.ReverseProxy{Director: director, ModifyResponse: modifyFunc, Transport: transport}
	server := http.Server{
		Addr: ":8080",
		Handler: proxy,
	}
	log.Fatal(server.ListenAndServe())
}
