package main

import (
	"net/http"
	"github.com/julienschmidt/httprouter"
	log "github.com/sirupsen/logrus"
	"encoding/json"
	"fmt"
	"os"
	"bytes"
	"io/ioutil"
	"context"
	"github.com/go-redis/redis/v8"

	"github.com/opentracing/opentracing-go"
	jaegercfg "github.com/uber/jaeger-client-go/config"
	"github.com/opentracing/opentracing-go/ext"
	
	"github.com/uber/jaeger-client-go/zipkin"
	jaegerlog "github.com/uber/jaeger-client-go/log"
	"github.com/uber/jaeger-lib/metrics"
)

const serviceName = "playlists-api"

var environment = os.Getenv("ENVIRONMENT")
var redis_host = os.Getenv("REDIS_HOST")
var redis_port = os.Getenv("REDIS_PORT")
var jaeger_host_port = os.Getenv("JAEGER_HOST_PORT")
var ctx = context.Background()
var rdb *redis.Client

func main() {

	 cfg := jaegercfg.Configuration{
		Sampler: &jaegercfg.SamplerConfig{
			Type:  "const",
			Param: 1,
		},

		// Log the emitted spans to stdout.
		Reporter: &jaegercfg.ReporterConfig{
			LogSpans: true,
			LocalAgentHostPort: jaeger_host_port,
		},
	}

	jLogger := jaegerlog.StdLogger
	jMetricsFactory := metrics.NullFactory

	zipkinPropagator := zipkin.NewZipkinB3HTTPHeaderPropagator()

	closer, err := cfg.InitGlobalTracer(
	  serviceName,
	  jaegercfg.Logger(jLogger),
	  jaegercfg.Metrics(jMetricsFactory),
	  jaegercfg.Injector(opentracing.HTTPHeaders, zipkinPropagator),
	  jaegercfg.Extractor(opentracing.HTTPHeaders, zipkinPropagator),
	  jaegercfg.ZipkinSharedRPCSpan(true),
	)


	if err != nil {
		panic(fmt.Sprintf("ERROR: cannot init Jaeger: %v\n", err))
	}
	defer closer.Close()

	router := httprouter.New()

	router.GET("/", func(w http.ResponseWriter, r *http.Request, p httprouter.Params){
		
		spanCtx, _ := opentracing.GlobalTracer().Extract(
			opentracing.HTTPHeaders,
			opentracing.HTTPHeadersCarrier(r.Header),
		)

		span := opentracing.StartSpan("/ GET", ext.RPCServerOption(spanCtx))
		defer span.Finish()

		cors(w)

		ctx := opentracing.ContextWithSpan(context.Background(), span)
		playlistsJson := getPlaylists(ctx)
		
		playlists := []playlist{}
		err := json.Unmarshal([]byte(playlistsJson), &playlists)
		if err != nil {
			panic(err)
		}

		//get videos for each playlist from videos api
		for pi := range playlists {

			vs := []videos{}
			for vi := range playlists[pi].Videos {

				videoSpan := opentracing.StartSpan("videos-api GET", opentracing.ChildOf(span.Context()))
				//span, _ := opentracing.StartSpanFromContext(ctx, "videos-api GET")
				v := videos{}
				
				req, err := http.NewRequest("GET", "http://videos-api:10010/" + playlists[pi].Videos[vi].Id, nil)
				if err != nil {
					panic(err)
				}

				span.Tracer().Inject(
					span.Context(),
					opentracing.HTTPHeaders,
					opentracing.HTTPHeadersCarrier(req.Header),
				)

				videoResp, err :=http.DefaultClient.Do(req)
				
				if err != nil {
					fmt.Println(err)
					videoSpan.SetTag("error", true)
					break
				}

				defer videoResp.Body.Close()
				videoSpan.Finish()
				
				video, err := ioutil.ReadAll(videoResp.Body)

				if err != nil {
					panic(err)
				}

				err = json.Unmarshal(video, &v)

				if err != nil {
					panic(err)
				}
				
				vs = append(vs, v)

			}

			playlists[pi].Videos = vs
		}

		playlistsBytes, err := json.Marshal(playlists)
		if err != nil {
			panic(err)
		}

		reader := bytes.NewReader(playlistsBytes)
		if b, err := ioutil.ReadAll(reader); err == nil {
			fmt.Fprintf(w, "%s", string(b))
		}

	})

	r := redis.NewClient(&redis.Options{
		Addr:     redis_host + ":" + redis_port,
		DB:       0,
	})
	rdb = r

	fmt.Println("Running...")
	log.Fatal(http.ListenAndServe(":10010", router))
}

func getPlaylists(ctx context.Context)(response string){

	span, _ := opentracing.StartSpanFromContext(ctx, "redis-get")
	defer span.Finish()
	playlistData, err := rdb.Get(ctx, "playlists").Result()
	
	if err != nil {
		fmt.Println(err)
		fmt.Println("error occured retrieving playlists from Redis")
		span.SetTag("error", true)
		return "[]"
	}

	return playlistData
}

type playlist struct {
	Id string `json:"id"`
	Name string `json:"name"`
	Videos []videos `json:"videos"`
}

type videos struct {
	Id string `json:"id"`
	Title string `json:"title"`
	Description string `json:"description"`
	Imageurl string `json:"imageurl"`
	Url string `json:"url"`

}

type stop struct {
	error
}

func cors(writer http.ResponseWriter) () {
	if(environment == "DEBUG"){
		writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-MY-API-Version")
		writer.Header().Set("Access-Control-Allow-Credentials", "true")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
	}
}