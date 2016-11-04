// This represents a peer in the network.
// the minimum that a peer requires to be "valid" is just an address.
// everything else can be discovered via the network.
// Just a bit of a wrapper for the client really, that contains most of the networking code, this mostly has the data and a few other things.

package zif

import (
	"bytes"
	"errors"
	"net"
	"time"

	"github.com/hashicorp/yamux"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ed25519"
)

type Peer struct {
	ZifAddress    Address
	PublicAddress string
	PublicKey     ed25519.PublicKey
	streams       StreamManager

	limiter *PeerLimiter

	entry *Entry
}

func (p *Peer) Announce(lp *LocalPeer) error {
	log.Debug("Sending announce to ", p.ZifAddress.Encode())

	if lp.Entry.PublicAddress == "" {
		log.Debug("Local peer public address is nil, attempting to fetch")
		ip := external_ip()
		log.Debug("External IP is ", ip)
		lp.Entry.PublicAddress = ip
	}
	lp.SignEntry()

	stream, err := p.OpenStream()

	if err != nil {
		return err
	}

	defer stream.Close()

	err = stream.Announce(&lp.Entry)

	return err
}

func (p *Peer) Connect(addr string, lp *LocalPeer) error {
	log.Debug("Peer connecting to ", addr)
	pair, err := p.streams.OpenTCP(addr, lp)

	if err != nil {
		return err
	}

	p.PublicKey = pair.pk
	p.ZifAddress = NewAddress(pair.pk)

	p.limiter = &PeerLimiter{}
	p.limiter.Setup()

	return nil
}

func (p *Peer) SetTCP(pair ConnHeader) {
	p.streams.connection = pair

	p.PublicKey = pair.pk
	p.ZifAddress = NewAddress(pair.pk)

	p.limiter = &PeerLimiter{}
	p.limiter.Setup()
}

func (p *Peer) ConnectServer() (*yamux.Session, error) {
	return p.streams.ConnectServer()
}

func (p *Peer) ConnectClient(lp *LocalPeer) (*yamux.Session, error) {
	client, err := p.streams.ConnectClient()

	if err != nil {
		return client, err
	}

	go lp.ListenStream(p)

	return client, err
}

func (p *Peer) GetSession() *yamux.Session {
	return p.streams.GetSession()
}

func (p *Peer) Terminate() {
	p.streams.Close()
}

func (p *Peer) OpenStream() (Client, error) {
	if p.GetSession() == nil {
		return Client{}, errors.New("Peer session nil")
	}

	if p.GetSession().IsClosed() {
		return Client{}, errors.New("Peer session closed")
	}
	return p.streams.OpenStream()
}

func (p *Peer) AddStream(conn net.Conn) {
	p.streams.AddStream(conn)
}

func (p *Peer) RemoveStream(conn net.Conn) {
	p.streams.RemoveStream(conn)
}

func (p *Peer) GetStream(conn net.Conn) *Client {
	return p.streams.GetStream(conn)
}

func (p *Peer) CloseStreams() {
	p.streams.Close()
}

func (p *Peer) Entry() (*Entry, error) {
	if p.entry != nil {
		return p.entry, nil
	}

	client, entries, err := p.Query(p.ZifAddress.Encode())
	defer client.Close()

	if err != nil {
		return nil, err
	}

	if len(entries) < 1 {
		return nil, errors.New("Query did not return an entry")
	}

	p.entry = &entries[0]

	return &entries[0], nil
}

func (p *Peer) Ping() bool {
	stream, err := p.OpenStream()
	defer stream.Close()

	if err != nil {
		log.Error(err.Error())
	}

	log.Info("Pinging ", p.ZifAddress.Encode())
	ret := stream.Ping(time.Second * 3)

	return ret

}

func (p *Peer) Bootstrap(rt *RoutingTable) (*Client, error) {
	log.Info("Bootstrapping from ", p.streams.connection.cl.conn.RemoteAddr())

	initial, err := p.Entry()

	if err != nil {
		return nil, err
	}
	rt.Update(*initial)

	stream, _ := p.OpenStream()

	return &stream, stream.Bootstrap(rt, rt.LocalAddress)
}

func (p *Peer) Query(address string) (*Client, []Entry, error) {
	log.WithField("target", address).Info("Querying")

	stream, _ := p.OpenStream()
	entry, err := stream.Query(address)
	return &stream, entry, err
}

// asks a peer to query its database and return the results
func (p *Peer) Search(search string, page int) ([]*Post, *Client, error) {
	stream, err := p.OpenStream()

	if err != nil {
		return nil, nil, err
	}

	posts, err := stream.Search(search, page)

	if err != nil {
		return nil, nil, err
	}

	return posts, &stream, nil
}

func (p *Peer) Recent(page int) ([]*Post, *Client, error) {
	stream, err := p.OpenStream()

	if err != nil {
		return nil, nil, err
	}

	posts, err := stream.Recent(page)

	return posts, &stream, err

}

func (p *Peer) Popular(page int) ([]*Post, *Client, error) {
	stream, err := p.OpenStream()

	if err != nil {
		return nil, nil, err
	}

	posts, err := stream.Popular(page)

	return posts, &stream, err

}

func (p *Peer) Mirror() (*Database, *Client, error) {
	col := Collection{}
	col.Setup()

	log.WithField("peer", p.ZifAddress.Encode()).Info("Mirroring")

	stream, err := p.OpenStream()

	if err != nil {
		return nil, nil, err
	}

	entry, err := p.Entry()

	if err != nil {
		return nil, nil, err
	}

	mcol, err := stream.Collection(entry.ZifAddress, entry.PublicKey)

	if err != nil {
		return nil, nil, err
	}

	for i := 0; i < mcol.Size; i++ {
		piece, err := stream.Piece(entry.ZifAddress, i)

		if err != nil {
			return nil, nil, err
		}

		hash, err := piece.Rehash()

		if err != nil {
			return nil, nil, err
		}

		if !bytes.Equal(mcol.HashList[32*i:32*i+32], hash) {
			return nil, nil, errors.New("Piece hash mismatch")
		}
	}

	return nil, &stream, err
}
