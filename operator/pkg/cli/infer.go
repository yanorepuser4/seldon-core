package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc/credentials/insecure"

	"encoding/binary"
	"encoding/json"

	grpc_retry "github.com/grpc-ecosystem/go-grpc-middleware/retry"
	"github.com/seldonio/seldon-core/scheduler/apis/mlops/v2_dataplane"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	SeldonModelHeader    = "seldon-model"
	SeldonRouteHeader    = "x-seldon-route"
	SeldonPipelineHeader = "pipeline"
	HeaderSeparator      = "="
)

const (
	tyBool   = "BOOL"
	tyUint8  = "UINT8"
	tyUint16 = "UINT16"
	tyUint32 = "UINT32"
	tyUint64 = "UINT64"
	tyInt8   = "INT8"
	tyInt16  = "INT16"
	tyInt32  = "INT32"
	tyInt64  = "INT64"
	tyFp16   = "FP16"
	tyFp32   = "FP32"
	tyFp64   = "FP64"
	tyBytes  = "BYTES"
)

type InferType uint32

const (
	InferModel InferType = iota
	InferPipeline
	InferExplainer
)

type InferenceClient struct {
	host        string
	httpClient  *http.Client
	callOptions []grpc.CallOption
	counts      map[string]int
}

type V2Error struct {
	Error string `json:"error"`
}

type V2InferenceResponse struct {
	ModelName    string                 `json:"model_name,omitempty"`
	ModelVersion string                 `json:"model_version,omitempty"`
	Id           string                 `json:"id"`
	Parameters   map[string]interface{} `json:"parameters,omitempty"`
	Outputs      []interface{}          `json:"outputs,omitempty"`
}

type V2Metadata struct {
	Name     string             `json:"name"`
	Versions []string           `json:"versions,omitempty"`
	Platform string             `json:"platform,omitempty"`
	Inputs   []V2MetadataTensor `json:"inputs,omitempty"`
	Outputs  []V2MetadataTensor `json:"outputs,omitempty"`
}

type V2MetadataTensor struct {
	Name     string `json:"name"`
	Datatype string `json:"datatype"`
	Shape    []int  `json:"shape"`
}

func NewInferenceClient(host string) *InferenceClient {
	opts := []grpc.CallOption{
		grpc.MaxCallSendMsgSize(math.MaxInt32),
		grpc.MaxCallRecvMsgSize(math.MaxInt32),
	}
	return &InferenceClient{
		host:        host,
		httpClient:  http.DefaultClient,
		callOptions: opts,
		counts:      make(map[string]int),
	}
}

func (ic *InferenceClient) getConnection() (*grpc.ClientConn, error) {
	retryOpts := []grpc_retry.CallOption{
		grpc_retry.WithBackoff(grpc_retry.BackoffExponential(100 * time.Millisecond)),
	}
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStreamInterceptor(grpc_retry.StreamClientInterceptor(retryOpts...)),
		grpc.WithUnaryInterceptor(grpc_retry.UnaryClientInterceptor(retryOpts...)),
	}
	conn, err := grpc.Dial(ic.host, opts...)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (ic *InferenceClient) getUrl(path string) *url.URL {
	return &url.URL{
		Scheme: "http",
		Host:   ic.host,
		Path:   path,
	}
}

func decodeV2Error(response *http.Response, b []byte) error {
	if response.StatusCode == http.StatusBadRequest {
		v2Error := V2Error{}
		err := json.Unmarshal(b, &v2Error)
		if err != nil {
			return err
		}
		return fmt.Errorf("%s", v2Error.Error)
	} else {
		return fmt.Errorf("V2 server error: %d %s", response.StatusCode, b)
	}

}

func (ic *InferenceClient) call(resourceName string, path string, data []byte, inferType InferType, showHeaders bool, headers []string, stickySessionKeys []string) ([]byte, error) {
	v2Url := ic.getUrl(path)
	req, err := http.NewRequest("POST", v2Url.String(), bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for _, stickySessionKey := range stickySessionKeys {
		req.Header.Add(SeldonRouteHeader, stickySessionKey)
	}
	switch inferType {
	case InferModel:
		req.Header.Set(SeldonModelHeader, resourceName)
	case InferPipeline:
		req.Header.Set(SeldonModelHeader, fmt.Sprintf("%s.%s", resourceName, SeldonPipelineHeader))
	}

	for _, header := range headers {
		parts := strings.Split(header, HeaderSeparator)
		if len(parts) != 2 {
			return nil, fmt.Errorf("Badly formed header %s: use key%sval", header, HeaderSeparator)
		}
		req.Header.Set(parts[0], parts[1])
	}

	if showHeaders {
		for k, v := range req.Header {
			fmt.Printf("Request header %s:%v\n", k, v)
		}
	}

	response, err := ic.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	err = response.Body.Close()
	if err != nil {
		return nil, err
	}
	if showHeaders {
		for k, v := range response.Header {
			fmt.Printf("Response header %s:%v\n", k, v)
		}
	}
	if response.StatusCode != http.StatusOK {
		return nil, decodeV2Error(response, b)
	}
	_, err = saveStickySessionKeyHttp(response.Header)
	ic.updateSummary(response.Header.Values(SeldonRouteHeader))
	if err != nil {
		return b, err
	}
	return b, nil
}

func (ic *InferenceClient) updateSummary(modelNames []string) {
	for _, modelName := range modelNames {
		if count, ok := ic.counts[modelName]; ok {
			ic.counts[modelName] = count + 1
		} else {
			ic.counts[modelName] = 1
		}
	}
}

func (ic *InferenceClient) ModelMetadata(modelName string) error {
	path := fmt.Sprintf("/v2/models/%s", modelName)
	v2Url := ic.getUrl(path)
	req, err := http.NewRequest("GET", v2Url.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set(SeldonModelHeader, modelName)
	response, err := ic.httpClient.Do(req)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	err = response.Body.Close()
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return decodeV2Error(response, b)
	}
	printPrettyJson(b)
	return nil
}

func (ic *InferenceClient) InferRest(resourceName string, data []byte, showRequest bool, showResponse bool, iterations int, inferType InferType, showHeaders bool, headers []string, stickySessionKeys []string) error {
	if showRequest {
		printPrettyJson(data)
	}
	path := fmt.Sprintf("/v2/models/%s/infer", resourceName)
	for i := 0; i < iterations; i++ {
		res, err := ic.call(resourceName, path, data, inferType, showHeaders, headers, stickySessionKeys)
		if err != nil {
			return err
		}
		v2InferResponse := V2InferenceResponse{}
		err = json.Unmarshal(res, &v2InferResponse)
		if err != nil {
			return err
		}
		if iterations == 1 {
			if showResponse {
				printPrettyJson(res)
			}
		}
	}
	if iterations > 1 {
		fmt.Printf("%v\n", ic.counts)
	}
	return nil
}

func getDataSize(shape []int64) int64 {
	tot := int64(1)
	for _, dim := range shape {
		tot = tot * dim
	}
	return tot
}

func updateResponseFromRawContents(res *v2_dataplane.ModelInferResponse) error {
	if len(res.RawOutputContents) == len(res.Outputs) {
		for idx, output := range res.Outputs {
			contents := &v2_dataplane.InferTensorContents{}
			output.Contents = contents
			var err error
			switch output.Datatype {
			case tyBool:
				output.Contents.BoolContents = make([]bool, getDataSize(output.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawOutputContents[idx]), binary.LittleEndian, &output.Contents.BoolContents)
			case tyUint8, tyUint16, tyUint32:
				output.Contents.UintContents = make([]uint32, getDataSize(output.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawOutputContents[idx]), binary.LittleEndian, &output.Contents.UintContents)
			case tyUint64:
				output.Contents.Uint64Contents = make([]uint64, getDataSize(output.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawOutputContents[idx]), binary.LittleEndian, &output.Contents.Uint64Contents)
			case tyInt8, tyInt16, tyInt32:
				output.Contents.IntContents = make([]int32, getDataSize(output.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawOutputContents[idx]), binary.LittleEndian, &output.Contents.IntContents)
			case tyInt64:
				output.Contents.Int64Contents = make([]int64, getDataSize(output.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawOutputContents[idx]), binary.LittleEndian, &output.Contents.Int64Contents)
			case tyFp16, tyFp32:
				output.Contents.Fp32Contents = make([]float32, getDataSize(output.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawOutputContents[idx]), binary.LittleEndian, &output.Contents.Fp32Contents)
			case tyFp64:
				output.Contents.Fp64Contents = make([]float64, getDataSize(output.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawOutputContents[idx]), binary.LittleEndian, &output.Contents.Fp64Contents)
			case tyBytes:
				output.Contents.BytesContents = make([][]byte, 1)
				output.Contents.BytesContents[0] = res.RawOutputContents[idx]
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func updateRequestFromRawContents(res *v2_dataplane.ModelInferRequest) error {
	if len(res.RawInputContents) == len(res.Inputs) {
		for idx, inputs := range res.Inputs {
			contents := &v2_dataplane.InferTensorContents{}
			inputs.Contents = contents
			var err error
			switch inputs.Datatype {
			case tyBool:
				inputs.Contents.BoolContents = make([]bool, getDataSize(inputs.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawInputContents[idx]), binary.LittleEndian, &inputs.Contents.BoolContents)
			case tyUint8, tyUint16, tyUint32:
				inputs.Contents.UintContents = make([]uint32, getDataSize(inputs.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawInputContents[idx]), binary.LittleEndian, &inputs.Contents.UintContents)
			case tyUint64:
				inputs.Contents.Uint64Contents = make([]uint64, getDataSize(inputs.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawInputContents[idx]), binary.LittleEndian, &inputs.Contents.Uint64Contents)
			case tyInt8, tyInt16, tyInt32:
				inputs.Contents.IntContents = make([]int32, getDataSize(inputs.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawInputContents[idx]), binary.LittleEndian, &inputs.Contents.IntContents)
			case tyInt64:
				inputs.Contents.Int64Contents = make([]int64, getDataSize(inputs.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawInputContents[idx]), binary.LittleEndian, &inputs.Contents.Int64Contents)
			case tyFp16, tyFp32:
				inputs.Contents.Fp32Contents = make([]float32, getDataSize(inputs.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawInputContents[idx]), binary.LittleEndian, &inputs.Contents.Fp32Contents)
			case tyFp64:
				inputs.Contents.Fp64Contents = make([]float64, getDataSize(inputs.Shape))
				err = binary.Read(bytes.NewBuffer(res.RawInputContents[idx]), binary.LittleEndian, &inputs.Contents.Fp64Contents)
			case tyBytes:
				inputs.Contents.BytesContents = make([][]byte, 1)
				inputs.Contents.BytesContents[0] = res.RawInputContents[idx]
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (ic *InferenceClient) InferGrpc(resourceName string, data []byte, showRequest bool, showResponse bool, iterations int, inferType InferType, showHeaders bool, headers []string, stickySessionKeys []string) error {
	req := &v2_dataplane.ModelInferRequest{}
	err := protojson.Unmarshal(data, req)
	if err != nil {
		return err
	}
	req.ModelName = resourceName
	if showRequest {
		printProto(req)
	}
	conn, err := ic.getConnection()
	if err != nil {
		return err
	}
	grpcClient := v2_dataplane.NewGRPCInferenceServiceClient(conn)
	ctx := context.TODO()
	for _, stickySessionKey := range stickySessionKeys {
		ctx = metadata.AppendToOutgoingContext(ctx, SeldonRouteHeader, stickySessionKey)
	}
	switch inferType {
	case InferModel:
		ctx = metadata.AppendToOutgoingContext(ctx, SeldonModelHeader, resourceName)
	case InferPipeline:
		ctx = metadata.AppendToOutgoingContext(ctx, SeldonModelHeader, fmt.Sprintf("%s.%s", resourceName, SeldonPipelineHeader))
	}

	for _, header := range headers {
		parts := strings.Split(header, HeaderSeparator)
		if len(parts) != 2 {
			return fmt.Errorf("Badly formed header %s: use key%sval", header, HeaderSeparator)
		}
		ctx = metadata.AppendToOutgoingContext(ctx, parts[0], parts[1])
	}

	if showHeaders {
		md, ok := metadata.FromOutgoingContext(ctx)
		if ok {
			for k, v := range md {
				fmt.Printf("Request metadata %s:%v\n", k, v)
			}
		}
	}

	for i := 0; i < iterations; i++ {
		var header, trailer metadata.MD
		res, err := grpcClient.ModelInfer(ctx, req, grpc.Header(&header), grpc.Trailer(&trailer))
		if err != nil {
			return err
		}
		if iterations == 1 {
			if showResponse {
				err := updateResponseFromRawContents(res)
				if err != nil {
					return err
				}
				printProto(res)
			}
		} else {
			ic.updateSummary(header.Get(SeldonRouteHeader))
		}
		_, err = saveStickySessionKeyGrpc(header)
		if err != nil {
			return err
		}
		if showHeaders {
			for k, v := range header {
				fmt.Printf("Response header %s:%v\n", k, v)
			}
			for k, v := range trailer {
				fmt.Printf("Response trailer %s:%v\n", k, v)
			}
		}
	}
	if iterations > 1 {
		fmt.Printf("%v\n", ic.counts)
	}
	return nil
}

func (ic *InferenceClient) Infer(modelName string, inferMode string, data []byte, showRequest bool, showResponse bool, iterations int, inferType InferType, showHeaders bool, headers []string, stickySesion bool) error {
	var stickySessionKeys []string
	var err error
	if stickySesion {
		stickySessionKeys, err = getStickySessionKeys()
		if err != nil {
			return err
		}
	}
	switch inferMode {
	case "rest":
		return ic.InferRest(modelName, data, showRequest, showResponse, iterations, inferType, showHeaders, headers, stickySessionKeys)
	case "grpc":
		return ic.InferGrpc(modelName, data, showRequest, showResponse, iterations, inferType, showHeaders, headers, stickySessionKeys)
	default:
		return fmt.Errorf("Unknown infer mode - needs to be grpc or rest")
	}
}