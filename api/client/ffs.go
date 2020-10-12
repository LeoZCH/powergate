package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	cid "github.com/ipfs/go-cid"
	files "github.com/ipfs/go-ipfs-files"
	httpapi "github.com/ipfs/go-ipfs-http-client"
	"github.com/ipfs/interface-go-ipfs-core/options"
	ipfspath "github.com/ipfs/interface-go-ipfs-core/path"
	"github.com/multiformats/go-multiaddr"
	"github.com/textileio/powergate/deals"
	"github.com/textileio/powergate/ffs"
	"github.com/textileio/powergate/ffs/rpc"
	"github.com/textileio/powergate/util"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FFS provides the API to create and interact with an FFS instance.
type FFS struct {
	client rpc.RPCServiceClient
}

// JobEvent represents an event for Watching a job.
type JobEvent struct {
	Job ffs.StorageJob
	Err error
}

// NewAddressOption is a function that changes a NewAddressConfig.
type NewAddressOption func(r *rpc.NewAddrRequest)

// WithMakeDefault specifies if the new address should become the default.
func WithMakeDefault(makeDefault bool) NewAddressOption {
	return func(r *rpc.NewAddrRequest) {
		r.MakeDefault = makeDefault
	}
}

// WithAddressType specifies the type of address to create.
func WithAddressType(addressType string) NewAddressOption {
	return func(r *rpc.NewAddrRequest) {
		r.AddressType = addressType
	}
}

// PushStorageConfigOption mutates a push request.
type PushStorageConfigOption func(r *rpc.PushStorageConfigRequest)

// WithStorageConfig overrides the Api default Cid configuration.
func WithStorageConfig(c ffs.StorageConfig) PushStorageConfigOption {
	return func(r *rpc.PushStorageConfigRequest) {
		r.HasConfig = true
		r.Config = &rpc.StorageConfig{
			Repairable: c.Repairable,
			Hot:        toRPCHotConfig(c.Hot),
			Cold:       toRPCColdConfig(c.Cold),
		}
	}
}

// WithOverride allows a new push configuration to override an existing one.
// It's used as an extra security measure to avoid unwanted configuration changes.
func WithOverride(override bool) PushStorageConfigOption {
	return func(r *rpc.PushStorageConfigRequest) {
		r.HasOverrideConfig = true
		r.OverrideConfig = override
	}
}

// WatchLogsOption is a function that changes GetLogsConfig.
type WatchLogsOption func(r *rpc.WatchLogsRequest)

// WithJidFilter filters only log messages of a Cid related to
// the Job with id jid.
func WithJidFilter(jid ffs.JobID) WatchLogsOption {
	return func(r *rpc.WatchLogsRequest) {
		r.Jid = jid.String()
	}
}

// WithHistory indicates that prior history logs should
// be sent in the channel before getting real time logs.
func WithHistory(enabled bool) WatchLogsOption {
	return func(r *rpc.WatchLogsRequest) {
		r.History = enabled
	}
}

// LogEvent represents an event for watching cid logs.
type LogEvent struct {
	LogEntry ffs.LogEntry
	Err      error
}

// ListDealRecordsOption updates a ListDealRecordsConfig.
type ListDealRecordsOption func(*rpc.ListDealRecordsConfig)

// WithFromAddrs limits the results deals initiated from the provided wallet addresses.
// If WithDataCids is also provided, this is an AND operation.
func WithFromAddrs(addrs ...string) ListDealRecordsOption {
	return func(c *rpc.ListDealRecordsConfig) {
		c.FromAddrs = addrs
	}
}

// WithDataCids limits the results to deals for the provided data cids.
// If WithFromAddrs is also provided, this is an AND operation.
func WithDataCids(cids ...string) ListDealRecordsOption {
	return func(c *rpc.ListDealRecordsConfig) {
		c.DataCids = cids
	}
}

// WithIncludePending specifies whether or not to include pending deals in the results. Default is false.
// Ignored for ListRetrievalDealRecords.
func WithIncludePending(includePending bool) ListDealRecordsOption {
	return func(c *rpc.ListDealRecordsConfig) {
		c.IncludePending = includePending
	}
}

// WithIncludeFinal specifies whether or not to include final deals in the results. Default is false.
// Ignored for ListRetrievalDealRecords.
func WithIncludeFinal(includeFinal bool) ListDealRecordsOption {
	return func(c *rpc.ListDealRecordsConfig) {
		c.IncludeFinal = includeFinal
	}
}

// WithAscending specifies to sort the results in ascending order. Default is descending order.
// Records are sorted by timestamp.
func WithAscending(ascending bool) ListDealRecordsOption {
	return func(c *rpc.ListDealRecordsConfig) {
		c.Ascending = ascending
	}
}

// ID returns the FFS instance ID.
func (f *FFS) ID(ctx context.Context) (ffs.APIID, error) {
	resp, err := f.client.ID(ctx, &rpc.IDRequest{})
	if err != nil {
		return ffs.EmptyInstanceID, err
	}
	return ffs.APIID(resp.Id), nil
}

// Addrs returns a list of addresses managed by the FFS instance.
func (f *FFS) Addrs(ctx context.Context) (*rpc.AddrsResponse, error) {
	return f.client.Addrs(ctx, &rpc.AddrsRequest{})
}

// DefaultStorageConfig returns the default storage config.
func (f *FFS) DefaultStorageConfig(ctx context.Context) (ffs.StorageConfig, error) {
	resp, err := f.client.DefaultStorageConfig(ctx, &rpc.DefaultStorageConfigRequest{})
	if err != nil {
		return ffs.StorageConfig{}, err
	}
	return fromRPCStorageConfig(resp.DefaultStorageConfig), nil
}

// SignMessage signs a message with a FFS managed wallet address.
func (f *FFS) SignMessage(ctx context.Context, addr string, message []byte) ([]byte, error) {
	r := &rpc.SignMessageRequest{Addr: addr, Msg: message}
	resp, err := f.client.SignMessage(ctx, r)
	return resp.Signature, err
}

// VerifyMessage verifies a message signature from a wallet address.
func (f *FFS) VerifyMessage(ctx context.Context, addr string, message, signature []byte) (bool, error) {
	r := &rpc.VerifyMessageRequest{Addr: addr, Msg: message, Signature: signature}
	resp, err := f.client.VerifyMessage(ctx, r)
	if err != nil {
		return false, err
	}
	return resp.Ok, nil
}

// NewAddr created a new wallet address managed by the FFS instance.
func (f *FFS) NewAddr(ctx context.Context, name string, options ...NewAddressOption) (string, error) {
	r := &rpc.NewAddrRequest{Name: name}
	for _, opt := range options {
		opt(r)
	}
	resp, err := f.client.NewAddr(ctx, r)
	return resp.Addr, err
}

// SetDefaultStorageConfig sets the default storage config.
func (f *FFS) SetDefaultStorageConfig(ctx context.Context, config ffs.StorageConfig) error {
	req := &rpc.SetDefaultStorageConfigRequest{
		Config: &rpc.StorageConfig{
			Hot:        toRPCHotConfig(config.Hot),
			Cold:       toRPCColdConfig(config.Cold),
			Repairable: config.Repairable,
		},
	}
	_, err := f.client.SetDefaultStorageConfig(ctx, req)
	return err
}

// CidData returns information about cids managed by the FFS instance.
func (f *FFS) CidData(ctx context.Context, cids ...string) (*rpc.CidDataResponse, error) {
	return f.client.CidData(ctx, &rpc.CidDataRequest{Cids: cids})
}

// CancelJob signals that the executing Job with JobID jid should be
// canceled.
func (f *FFS) CancelJob(ctx context.Context, jid ffs.JobID) error {
	_, err := f.client.CancelJob(ctx, &rpc.CancelJobRequest{Jid: jid.String()})
	return err
}

// StorageJob returns the current state of the specified job.
func (f *FFS) StorageJob(ctx context.Context, jid ffs.JobID) (*rpc.StorageJobResponse, error) {
	return f.client.StorageJob(ctx, &rpc.StorageJobRequest{Jid: jid.String()})
}

// QueuedStorageJobs returns a list of queued storage jobs.
func (f *FFS) QueuedStorageJobs(ctx context.Context, cids ...string) (*rpc.QueuedStorageJobsResponse, error) {
	req := &rpc.QueuedStorageJobsRequest{
		Cids: cids,
	}
	return f.client.QueuedStorageJobs(ctx, req)
}

// ExecutingStorageJobs returns a list of executing storage jobs.
func (f *FFS) ExecutingStorageJobs(ctx context.Context, cids ...string) (*rpc.ExecutingStorageJobsResponse, error) {
	req := &rpc.ExecutingStorageJobsRequest{
		Cids: cids,
	}
	return f.client.ExecutingStorageJobs(ctx, req)
}

// LatestFinalStorageJobs returns a list of latest final storage jobs.
func (f *FFS) LatestFinalStorageJobs(ctx context.Context, cids ...string) (*rpc.LatestFinalStorageJobsResponse, error) {
	req := &rpc.LatestFinalStorageJobsRequest{
		Cids: cids,
	}
	return f.client.LatestFinalStorageJobs(ctx, req)
}

// LatestSuccessfulStorageJobs returns a list of latest successful storage jobs.
func (f *FFS) LatestSuccessfulStorageJobs(ctx context.Context, cids ...string) (*rpc.LatestSuccessfulStorageJobsResponse, error) {
	req := &rpc.LatestSuccessfulStorageJobsRequest{
		Cids: cids,
	}
	return f.client.LatestSuccessfulStorageJobs(ctx, req)
}

// StorageJobsSummary returns a summary of storage jobs.
func (f *FFS) StorageJobsSummary(ctx context.Context, cids ...string) (*rpc.StorageJobsSummaryResponse, error) {
	req := &rpc.StorageJobsSummaryRequest{
		Cids: cids,
	}
	return f.client.StorageJobsSummary(ctx, req)
}

// WatchJobs pushes JobEvents to the provided channel. The provided channel will be owned
// by the client after the call, so it shouldn't be closed by the client. To stop receiving
// events, the provided ctx should be canceled. If an error occurs, it will be returned
// in the Err field of JobEvent and the channel will be closed.
func (f *FFS) WatchJobs(ctx context.Context, ch chan<- JobEvent, jids ...ffs.JobID) error {
	jidStrings := make([]string, len(jids))
	for i, jid := range jids {
		jidStrings[i] = jid.String()
	}

	stream, err := f.client.WatchJobs(ctx, &rpc.WatchJobsRequest{Jids: jidStrings})
	if err != nil {
		return err
	}
	go func() {
		for {
			reply, err := stream.Recv()
			if err == io.EOF || status.Code(err) == codes.Canceled {
				close(ch)
				break
			}
			if err != nil {
				ch <- JobEvent{Err: err}
				close(ch)
				break
			}

			job, err := fromRPCJob(reply.Job)
			if err != nil {
				ch <- JobEvent{Err: err}
				close(ch)
				break
			}

			ch <- JobEvent{Job: job}
		}
	}()
	return nil
}

// Replace pushes a StorageConfig for c2 equal to that of c1, and removes c1. This operation
// is more efficient than manually removing and adding in two separate operations.
func (f *FFS) Replace(ctx context.Context, c1 cid.Cid, c2 cid.Cid) (ffs.JobID, error) {
	resp, err := f.client.Replace(ctx, &rpc.ReplaceRequest{Cid1: util.CidToString(c1), Cid2: util.CidToString(c2)})
	if err != nil {
		return ffs.EmptyJobID, err
	}
	return ffs.JobID(resp.JobId), nil
}

// PushStorageConfig push a new configuration for the Cid in the Hot and Cold layers.
func (f *FFS) PushStorageConfig(ctx context.Context, c cid.Cid, opts ...PushStorageConfigOption) (ffs.JobID, error) {
	req := &rpc.PushStorageConfigRequest{Cid: util.CidToString(c)}
	for _, opt := range opts {
		opt(req)
	}

	resp, err := f.client.PushStorageConfig(ctx, req)
	if err != nil {
		return ffs.EmptyJobID, err
	}

	return ffs.JobID(resp.JobId), nil
}

// Remove removes a Cid from being tracked as an active storage. The Cid should have
// both Hot and Cold storage disabled, if that isn't the case it will return ErrActiveInStorage.
func (f *FFS) Remove(ctx context.Context, c cid.Cid) error {
	_, err := f.client.Remove(ctx, &rpc.RemoveRequest{Cid: util.CidToString(c)})
	return err
}

// GetFolder retrieves to outputDir a Cid which corresponds to a folder.
func (f *FFS) GetFolder(ctx context.Context, ipfsRevProxyAddr string, c cid.Cid, outputDir string) error {
	token := ctx.Value(AuthKey).(string)
	ipfs, err := newDecoratedIPFSAPI(ipfsRevProxyAddr, token)
	if err != nil {
		return fmt.Errorf("creating decorated IPFS client: %s", err)
	}
	n, err := ipfs.Unixfs().Get(ctx, ipfspath.IpfsPath(c))
	if err != nil {
		return fmt.Errorf("getting folder DAG from IPFS: %s", err)
	}
	err = files.WriteTo(n, outputDir)
	if err != nil {
		return fmt.Errorf("saving folder DAG to output folder: %s", err)
	}
	return nil
}

// Get returns an io.Reader for reading a stored Cid from the Hot Storage.
func (f *FFS) Get(ctx context.Context, c cid.Cid) (io.Reader, error) {
	stream, err := f.client.Get(ctx, &rpc.GetRequest{
		Cid: util.CidToString(c),
	})
	if err != nil {
		return nil, err
	}
	reader, writer := io.Pipe()
	go func() {
		for {
			reply, err := stream.Recv()
			if err == io.EOF {
				_ = writer.Close()
				break
			} else if err != nil {
				_ = writer.CloseWithError(err)
				break
			}
			_, err = writer.Write(reply.GetChunk())
			if err != nil {
				_ = writer.CloseWithError(err)
				break
			}
		}
	}()

	return reader, nil
}

// WatchLogs pushes human-friendly messages about Cid executions. The method is blocking
// and will continue to send messages until the context is canceled. The provided channel
// is owned by the method and must not be closed.
func (f *FFS) WatchLogs(ctx context.Context, ch chan<- LogEvent, c cid.Cid, opts ...WatchLogsOption) error {
	r := &rpc.WatchLogsRequest{Cid: util.CidToString(c)}
	for _, opt := range opts {
		opt(r)
	}

	stream, err := f.client.WatchLogs(ctx, r)
	if err != nil {
		return err
	}
	go func() {
		for {
			reply, err := stream.Recv()
			if err == io.EOF || status.Code(err) == codes.Canceled {
				close(ch)
				break
			}
			if err != nil {
				ch <- LogEvent{Err: err}
				close(ch)
				break
			}

			cid, err := util.CidFromString(reply.LogEntry.Cid)
			if err != nil {
				ch <- LogEvent{Err: err}
				close(ch)
				break
			}

			entry := ffs.LogEntry{
				Cid:       cid,
				Timestamp: time.Unix(reply.LogEntry.Time, 0),
				Jid:       ffs.JobID(reply.LogEntry.Jid),
				Msg:       reply.LogEntry.Msg,
			}
			ch <- LogEvent{LogEntry: entry}
		}
	}()
	return nil
}

// SendFil sends fil from a managed address to any another address, returns immediately but funds are sent asynchronously.
func (f *FFS) SendFil(ctx context.Context, from string, to string, amount int64) error {
	req := &rpc.SendFilRequest{
		From:   from,
		To:     to,
		Amount: amount,
	}
	_, err := f.client.SendFil(ctx, req)
	return err
}

// Stage allows to temporarily stage data in the Hot Storage in preparation for pushing a cid storage config.
func (f *FFS) Stage(ctx context.Context, data io.Reader) (*cid.Cid, error) {
	stream, err := f.client.Stage(ctx)
	if err != nil {
		return nil, err
	}

	buffer := make([]byte, 1024*32) // 32KB
	for {
		bytesRead, err := data.Read(buffer)
		if err != nil && err != io.EOF {
			return nil, err
		}
		sendErr := stream.Send(&rpc.StageRequest{Chunk: buffer[:bytesRead]})
		if sendErr != nil {
			if sendErr == io.EOF {
				var noOp interface{}
				return nil, stream.RecvMsg(noOp)
			}
			return nil, sendErr
		}
		if err == io.EOF {
			break
		}
	}
	reply, err := stream.CloseAndRecv()
	if err != nil {
		return nil, err
	}

	cid, err := util.CidFromString(reply.GetCid())
	if err != nil {
		return nil, err
	}
	return &cid, nil
}

// StageFolder allows to temporarily stage a folder in the Hot Storage in preparation for pushing a cid storage config.
func (f *FFS) StageFolder(ctx context.Context, ipfsRevProxyAddr string, folderPath string) (cid.Cid, error) {
	ffsToken := ctx.Value(AuthKey).(string)

	ipfs, err := newDecoratedIPFSAPI(ipfsRevProxyAddr, ffsToken)
	if err != nil {
		return cid.Undef, fmt.Errorf("creating IPFS HTTP client: %s", err)
	}

	stat, err := os.Lstat(folderPath)
	if err != nil {
		return cid.Undef, err
	}
	ff, err := files.NewSerialFile(folderPath, false, stat)
	if err != nil {
		return cid.Undef, err
	}
	defer func() { _ = ff.Close() }()
	opts := []options.UnixfsAddOption{
		options.Unixfs.CidVersion(1),
		options.Unixfs.Pin(false),
	}
	pth, err := ipfs.Unixfs().Add(context.Background(), files.ToDir(ff), opts...)
	if err != nil {
		return cid.Undef, err
	}

	return pth.Cid(), nil
}

// ListPayChannels returns a list of payment channels.
func (f *FFS) ListPayChannels(ctx context.Context) ([]ffs.PaychInfo, error) {
	resp, err := f.client.ListPayChannels(ctx, &rpc.ListPayChannelsRequest{})
	if err != nil {
		return []ffs.PaychInfo{}, err
	}
	infos := make([]ffs.PaychInfo, len(resp.PayChannels))
	for i, info := range resp.PayChannels {
		infos[i] = fromRPCPaychInfo(info)
	}
	return infos, nil
}

// CreatePayChannel creates a new payment channel.
func (f *FFS) CreatePayChannel(ctx context.Context, from string, to string, amount uint64) (ffs.PaychInfo, cid.Cid, error) {
	req := &rpc.CreatePayChannelRequest{
		From:   from,
		To:     to,
		Amount: amount,
	}
	resp, err := f.client.CreatePayChannel(ctx, req)
	if err != nil {
		return ffs.PaychInfo{}, cid.Undef, err
	}
	messageCid, err := util.CidFromString(resp.ChannelMessageCid)
	if err != nil {
		return ffs.PaychInfo{}, cid.Undef, err
	}
	return fromRPCPaychInfo(resp.PayChannel), messageCid, nil
}

// RedeemPayChannel redeems a payment channel.
func (f *FFS) RedeemPayChannel(ctx context.Context, addr string) error {
	req := &rpc.RedeemPayChannelRequest{PayChannelAddr: addr}
	_, err := f.client.RedeemPayChannel(ctx, req)
	return err
}

// ListStorageDealRecords returns a list of storage deals for the FFS instance according to the provided options.
func (f *FFS) ListStorageDealRecords(ctx context.Context, opts ...ListDealRecordsOption) ([]deals.StorageDealRecord, error) {
	conf := &rpc.ListDealRecordsConfig{}
	for _, opt := range opts {
		opt(conf)
	}
	res, err := f.client.ListStorageDealRecords(ctx, &rpc.ListStorageDealRecordsRequest{Config: conf})
	if err != nil {
		return nil, fmt.Errorf("calling ListStorageDealRecords: %v", err)
	}
	ret, err := fromRPCStorageDealRecords(res.Records)
	if err != nil {
		return nil, fmt.Errorf("processing response deal records: %v", err)
	}
	return ret, nil
}

// ListRetrievalDealRecords returns a list of retrieval deals for the FFS instance according to the provided options.
func (f *FFS) ListRetrievalDealRecords(ctx context.Context, opts ...ListDealRecordsOption) ([]deals.RetrievalDealRecord, error) {
	conf := &rpc.ListDealRecordsConfig{}
	for _, opt := range opts {
		opt(conf)
	}
	res, err := f.client.ListRetrievalDealRecords(ctx, &rpc.ListRetrievalDealRecordsRequest{Config: conf})
	if err != nil {
		return nil, fmt.Errorf("calling ListRetrievalDealRecords: %v", err)
	}
	ret, err := fromRPCRetrievalDealRecords(res.Records)
	if err != nil {
		return nil, fmt.Errorf("processing response deal records: %v", err)
	}
	return ret, nil
}

func toRPCHotConfig(config ffs.HotConfig) *rpc.HotConfig {
	return &rpc.HotConfig{
		Enabled:          config.Enabled,
		AllowUnfreeze:    config.AllowUnfreeze,
		UnfreezeMaxPrice: config.UnfreezeMaxPrice,
		Ipfs: &rpc.IpfsConfig{
			AddTimeout: int64(config.Ipfs.AddTimeout),
		},
	}
}

func toRPCColdConfig(config ffs.ColdConfig) *rpc.ColdConfig {
	return &rpc.ColdConfig{
		Enabled: config.Enabled,
		Filecoin: &rpc.FilConfig{
			RepFactor:       int64(config.Filecoin.RepFactor),
			DealMinDuration: config.Filecoin.DealMinDuration,
			ExcludedMiners:  config.Filecoin.ExcludedMiners,
			TrustedMiners:   config.Filecoin.TrustedMiners,
			CountryCodes:    config.Filecoin.CountryCodes,
			Renew: &rpc.FilRenew{
				Enabled:   config.Filecoin.Renew.Enabled,
				Threshold: int64(config.Filecoin.Renew.Threshold),
			},
			Addr:            config.Filecoin.Addr,
			DealStartOffset: config.Filecoin.DealStartOffset,
			FastRetrieval:   config.Filecoin.FastRetrieval,
			MaxPrice:        config.Filecoin.MaxPrice,
		},
	}
}

func fromRPCStorageConfig(config *rpc.StorageConfig) ffs.StorageConfig {
	if config == nil {
		return ffs.StorageConfig{}
	}
	ret := ffs.StorageConfig{Repairable: config.Repairable}
	if config.Hot != nil {
		ret.Hot = ffs.HotConfig{
			Enabled:          config.Hot.Enabled,
			AllowUnfreeze:    config.Hot.AllowUnfreeze,
			UnfreezeMaxPrice: config.Hot.UnfreezeMaxPrice,
		}
		if config.Hot.Ipfs != nil {
			ret.Hot.Ipfs = ffs.IpfsConfig{
				AddTimeout: int(config.Hot.Ipfs.AddTimeout),
			}
		}
	}
	if config.Cold != nil {
		ret.Cold = ffs.ColdConfig{
			Enabled: config.Cold.Enabled,
		}
		if config.Cold.Filecoin != nil {
			ret.Cold.Filecoin = ffs.FilConfig{
				RepFactor:       int(config.Cold.Filecoin.RepFactor),
				DealMinDuration: config.Cold.Filecoin.DealMinDuration,
				ExcludedMiners:  config.Cold.Filecoin.ExcludedMiners,
				CountryCodes:    config.Cold.Filecoin.CountryCodes,
				TrustedMiners:   config.Cold.Filecoin.TrustedMiners,
				Addr:            config.Cold.Filecoin.Addr,
				MaxPrice:        config.Cold.Filecoin.MaxPrice,
				FastRetrieval:   config.Cold.Filecoin.FastRetrieval,
				DealStartOffset: config.Cold.Filecoin.DealStartOffset,
			}
			if config.Cold.Filecoin.Renew != nil {
				ret.Cold.Filecoin.Renew = ffs.FilRenew{
					Enabled:   config.Cold.Filecoin.Renew.Enabled,
					Threshold: int(config.Cold.Filecoin.Renew.Threshold),
				}
			}
		}
	}
	return ret
}

func fromRPCDealErrors(des []*rpc.DealError) ([]ffs.DealError, error) {
	res := make([]ffs.DealError, len(des))
	for i, de := range des {
		var propCid cid.Cid
		if de.ProposalCid != "" && de.ProposalCid != "b" {
			var err error
			propCid, err = util.CidFromString(de.ProposalCid)
			if err != nil {
				return nil, fmt.Errorf("proposal cid is invalid")
			}
		}
		res[i] = ffs.DealError{
			ProposalCid: propCid,
			Miner:       de.Miner,
			Message:     de.Message,
		}
	}
	return res, nil
}

func fromRPCPaychInfo(info *rpc.PaychInfo) ffs.PaychInfo {
	var direction ffs.PaychDir
	switch info.Direction {
	case rpc.Direction_DIRECTION_INBOUND:
		direction = ffs.PaychDirInbound
	case rpc.Direction_DIRECTION_OUTBOUND:
		direction = ffs.PaychDirOutbound
	default:
		direction = ffs.PaychDirUnspecified
	}
	return ffs.PaychInfo{
		CtlAddr:   info.CtlAddr,
		Addr:      info.Addr,
		Direction: direction,
	}
}

func fromRPCStorageDealRecords(records []*rpc.StorageDealRecord) ([]deals.StorageDealRecord, error) {
	var ret []deals.StorageDealRecord
	for _, rpcRecord := range records {
		if rpcRecord.DealInfo == nil {
			continue
		}
		rootCid, err := util.CidFromString(rpcRecord.RootCid)
		if err != nil {
			return nil, err
		}
		record := deals.StorageDealRecord{
			RootCid: rootCid,
			Addr:    rpcRecord.Addr,
			Time:    rpcRecord.Time,
			Pending: rpcRecord.Pending,
		}
		proposalCid, err := util.CidFromString(rpcRecord.DealInfo.ProposalCid)
		if err != nil {
			return nil, err
		}
		pieceCid, err := util.CidFromString(rpcRecord.DealInfo.PieceCid)
		if err != nil {
			return nil, err
		}
		record.DealInfo = deals.StorageDealInfo{
			ProposalCid:     proposalCid,
			StateID:         rpcRecord.DealInfo.StateId,
			StateName:       rpcRecord.DealInfo.StateName,
			Miner:           rpcRecord.DealInfo.Miner,
			PieceCID:        pieceCid,
			Size:            rpcRecord.DealInfo.Size,
			PricePerEpoch:   rpcRecord.DealInfo.PricePerEpoch,
			StartEpoch:      rpcRecord.DealInfo.StartEpoch,
			Duration:        rpcRecord.DealInfo.Duration,
			DealID:          rpcRecord.DealInfo.DealId,
			ActivationEpoch: rpcRecord.DealInfo.ActivationEpoch,
			Message:         rpcRecord.DealInfo.Msg,
		}
		ret = append(ret, record)
	}
	return ret, nil
}

func fromRPCRetrievalDealRecords(records []*rpc.RetrievalDealRecord) ([]deals.RetrievalDealRecord, error) {
	var ret []deals.RetrievalDealRecord
	for _, rpcRecord := range records {
		if rpcRecord.DealInfo == nil {
			continue
		}
		record := deals.RetrievalDealRecord{
			Addr: rpcRecord.Addr,
			Time: rpcRecord.Time,
		}
		rootCid, err := util.CidFromString(rpcRecord.DealInfo.RootCid)
		if err != nil {
			return nil, err
		}
		record.DealInfo = deals.RetrievalDealInfo{
			RootCid:                 rootCid,
			Size:                    rpcRecord.DealInfo.Size,
			MinPrice:                rpcRecord.DealInfo.MinPrice,
			PaymentInterval:         rpcRecord.DealInfo.PaymentInterval,
			PaymentIntervalIncrease: rpcRecord.DealInfo.PaymentIntervalIncrease,
			Miner:                   rpcRecord.DealInfo.Miner,
			MinerPeerID:             rpcRecord.DealInfo.MinerPeerId,
		}
		ret = append(ret, record)
	}
	return ret, nil
}

func fromRPCJob(job *rpc.Job) (ffs.StorageJob, error) {
	c, err := util.CidFromString(job.Cid)
	if err != nil {
		return ffs.StorageJob{}, err
	}

	var dealInfos []deals.StorageDealInfo
	for _, item := range job.DealInfo {
		proposalCid, err := util.CidFromString(item.ProposalCid)
		if err != nil {
			return ffs.StorageJob{}, err
		}
		pieceCid, err := util.CidFromString(item.PieceCid)
		if err != nil {
			return ffs.StorageJob{}, err
		}
		dealInfo := deals.StorageDealInfo{
			ActivationEpoch: item.ActivationEpoch,
			DealID:          item.DealId,
			Duration:        item.Duration,
			Message:         item.Message,
			Miner:           item.Miner,
			PieceCID:        pieceCid,
			PricePerEpoch:   item.PricePerEpoch,
			ProposalCid:     proposalCid,
			Size:            item.Size,
			StartEpoch:      item.StartEpoch,
			StateID:         item.StateId,
			StateName:       item.StateName,
		}
		dealInfos = append(dealInfos, dealInfo)
	}

	dealErrors, err := fromRPCDealErrors(job.DealErrors)
	if err != nil {
		return ffs.StorageJob{}, err
	}

	var status ffs.JobStatus
	switch job.Status {
	case rpc.JobStatus_JOB_STATUS_UNSPECIFIED:
		status = ffs.Unspecified
	case rpc.JobStatus_JOB_STATUS_QUEUED:
		status = ffs.Queued
	case rpc.JobStatus_JOB_STATUS_EXECUTING:
		status = ffs.Executing
	case rpc.JobStatus_JOB_STATUS_FAILED:
		status = ffs.Failed
	case rpc.JobStatus_JOB_STATUS_CANCELED:
		status = ffs.Canceled
	case rpc.JobStatus_JOB_STATUS_SUCCESS:
		status = ffs.Success
	default:
		return ffs.StorageJob{}, fmt.Errorf("unknown job status: %v", job.Status.String())
	}
	return ffs.StorageJob{
		ID:         ffs.JobID(job.Id),
		APIID:      ffs.APIID(job.ApiId),
		Cid:        c,
		Status:     status,
		ErrCause:   job.ErrCause,
		DealInfo:   dealInfos,
		DealErrors: dealErrors,
		CreatedAt:  job.CreatedAt,
	}, nil
}

func newDecoratedIPFSAPI(proxyAddr, ffsToken string) (*httpapi.HttpApi, error) {
	ipport := strings.Split(proxyAddr, ":")
	if len(ipport) != 2 {
		return nil, fmt.Errorf("ipfs addr is invalid")
	}
	cm, err := multiaddr.NewComponent("dns4", ipport[0])
	if err != nil {
		return nil, err
	}
	cp, err := multiaddr.NewComponent("tcp", ipport[1])
	if err != nil {
		return nil, err
	}
	useHTTPS := ipport[1] == "443"
	ipfsMaddr := cm.Encapsulate(cp)
	customClient := http.DefaultClient
	customClient.Transport = newFFSHeaderDecorator(ffsToken, useHTTPS)
	ipfs, err := httpapi.NewApiWithClient(ipfsMaddr, customClient)
	if err != nil {
		return nil, err
	}
	return ipfs, nil
}

type ffsHeaderDecorator struct {
	ffsToken string
	useHTTPS bool
}

func newFFSHeaderDecorator(ffsToken string, useHTTPS bool) *ffsHeaderDecorator {
	return &ffsHeaderDecorator{
		ffsToken: ffsToken,
		useHTTPS: useHTTPS,
	}
}

func (fhd ffsHeaderDecorator) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header["x-ipfs-ffs-auth"] = []string{fhd.ffsToken}
	if fhd.useHTTPS {
		req.URL.Scheme = "https"
	}

	return http.DefaultTransport.RoundTrip(req)
}
