package coreapi

import (
	"context"
	"fmt"

	"github.com/ipfs/go-ipfs/core"
	"github.com/ipfs/go-ipfs/filestore"

	coreiface "github.com/ipfs/go-ipfs/core/coreapi/interface"
	"github.com/ipfs/go-ipfs/core/coreapi/interface/options"
	"github.com/ipfs/go-ipfs/core/coreunix"

	cidutil "gx/ipfs/QmPLrvCg3Pjbc77VeeLEY5Si39SuAGhaN1X38pXo7zUXzx/go-cidutil"
	blockservice "gx/ipfs/QmPoh3SrQzFBWtdGK6qmHDV4EanKR6kYPj4DD3J2NLoEmZ/go-blockservice"
	bstore "gx/ipfs/QmS2aqUZLJp8kF1ihE5rvDGE5LvmKDPnx32w9Z1BW9xLV5/go-ipfs-blockstore"
	mfs "gx/ipfs/QmU3iDRUrxyTYdV2j5MuWLFvP1k7w98vD66PLnNChgvUmZ/go-mfs"
	files "gx/ipfs/QmXWZCd8jfaHmt4UDSnjKmGcrQMw95bDGWqEeVLVJjoANX/go-ipfs-files"
	ft "gx/ipfs/Qmbvw7kpSM2p6rbQ57WGRhhqNfCiNGW6EKH4xgHLw4bsnB/go-unixfs"
	uio "gx/ipfs/Qmbvw7kpSM2p6rbQ57WGRhhqNfCiNGW6EKH4xgHLw4bsnB/go-unixfs/io"
	ipld "gx/ipfs/QmcKKBwfz6FyQdHR2jsXrrF6XeSBXYL86anmWNewpFpoF5/go-ipld-format"
	dag "gx/ipfs/QmdV35UHnL1FM52baPkeUo6u7Fxm2CRUkPTLRPxeF8a4Ap/go-merkledag"
	dagtest "gx/ipfs/QmdV35UHnL1FM52baPkeUo6u7Fxm2CRUkPTLRPxeF8a4Ap/go-merkledag/test"
)

type UnixfsAPI CoreAPI

// Add builds a merkledag node from a reader, adds it to the blockstore,
// and returns the key representing that node.
func (api *UnixfsAPI) Add(ctx context.Context, files files.Node, opts ...options.UnixfsAddOption) (coreiface.ResolvedPath, error) {
	settings, prefix, err := options.UnixfsAddOptions(opts...)
	if err != nil {
		return nil, err
	}

	cfg, err := api.repo.Config()
	if err != nil {
		return nil, err
	}

	// check if repo will exceed storage limit if added
	// TODO: this doesn't handle the case if the hashed file is already in blocks (deduplicated)
	// TODO: conditional GC is disabled due to it is somehow not possible to pass the size to the daemon
	//if err := corerepo.ConditionalGC(req.Context(), n, uint64(size)); err != nil {
	//	res.SetError(err, cmdkit.ErrNormal)
	//	return
	//}

	if settings.NoCopy && !cfg.Experimental.FilestoreEnabled {
		return nil, filestore.ErrFilestoreNotEnabled
	}

	addblockstore := api.blockstore
	if !(settings.FsCache || settings.NoCopy) {
		addblockstore = bstore.NewGCBlockstore(api.baseBlocks, api.blockstore)
	}
	exch := api.exchange
	pinning := api.pinning

	if settings.OnlyHash {
		nilnode, err := core.NewNode(ctx, &core.BuildCfg{
			//TODO: need this to be true or all files
			// hashed will be stored in memory!
			NilRepo: true,
		})
		if err != nil {
			return nil, err
		}
		addblockstore = nilnode.Blockstore
		exch = nilnode.Exchange
		pinning = nilnode.Pinning
	}

	bserv := blockservice.New(addblockstore, exch) // hash security 001
	dserv := dag.NewDAGService(bserv)

	fileAdder, err := coreunix.NewAdder(ctx, pinning, addblockstore, dserv)
	if err != nil {
		return nil, err
	}

	fileAdder.Chunker = settings.Chunker
	if settings.Events != nil {
		fileAdder.Out = settings.Events
		fileAdder.Progress = settings.Progress
	}
	fileAdder.Hidden = settings.Hidden
	fileAdder.Wrap = settings.Wrap
	fileAdder.Pin = settings.Pin && !settings.OnlyHash
	fileAdder.Silent = settings.Silent
	fileAdder.RawLeaves = settings.RawLeaves
	fileAdder.NoCopy = settings.NoCopy
	fileAdder.Name = settings.StdinName
	fileAdder.CidBuilder = prefix

	switch settings.Layout {
	case options.BalancedLayout:
		// Default
	case options.TrickleLayout:
		fileAdder.Trickle = true
	default:
		return nil, fmt.Errorf("unknown layout: %d", settings.Layout)
	}

	if settings.Inline {
		fileAdder.CidBuilder = cidutil.InlineBuilder{
			Builder: fileAdder.CidBuilder,
			Limit:   settings.InlineLimit,
		}
	}

	if settings.OnlyHash {
		md := dagtest.Mock()
		emptyDirNode := ft.EmptyDirNode()
		// Use the same prefix for the "empty" MFS root as for the file adder.
		emptyDirNode.SetCidBuilder(fileAdder.CidBuilder)
		mr, err := mfs.NewRoot(ctx, md, emptyDirNode, nil)
		if err != nil {
			return nil, err
		}

		fileAdder.SetMfsRoot(mr)
	}

	nd, err := fileAdder.AddAllAndPin(files)
	if err != nil {
		return nil, err
	}
	return coreiface.IpfsPath(nd.Cid()), nil
}

func (api *UnixfsAPI) Get(ctx context.Context, p coreiface.Path) (files.Node, error) {
	ses := api.core().getSession(ctx)

	nd, err := ses.ResolveNode(ctx, p)
	if err != nil {
		return nil, err
	}

	return newUnixfsFile(ctx, ses.dag, nd)
}

// Ls returns the contents of an IPFS or IPNS object(s) at path p, with the format:
// `<link base58 hash> <link size in bytes> <link name>`
func (api *UnixfsAPI) Ls(ctx context.Context, p coreiface.Path) ([]*ipld.Link, error) {
	dagnode, err := api.core().ResolveNode(ctx, p)
	if err != nil {
		return nil, err
	}

	var ndlinks []*ipld.Link
	dir, err := uio.NewDirectoryFromNode(api.dag, dagnode)
	switch err {
	case nil:
		l, err := dir.Links(ctx)
		if err != nil {
			return nil, err
		}
		ndlinks = l
	case uio.ErrNotADir:
		ndlinks = dagnode.Links()
	default:
		return nil, err
	}

	links := make([]*ipld.Link, len(ndlinks))
	for i, l := range ndlinks {
		links[i] = &ipld.Link{Name: l.Name, Size: l.Size, Cid: l.Cid}
	}
	return links, nil
}

func (api *UnixfsAPI) core() *CoreAPI {
	return (*CoreAPI)(api)
}
