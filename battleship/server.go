package battleship

import (
	"fmt"
	"log"
	"sync"

	"golang.org/x/net/context"
	"google.golang.org/grpc"

	"github.com/bobinette/deadpool/battleship/proto"
)

type Client struct {
	Name string
	ID   int32

	leave chan struct{}
}

type Server struct {
	blue    *Client
	red     *Client
	lastID  int32
	current int32
	locker  sync.Locker

	game     *Game
	notifier Notifier
	plies    int
}

func NewServer() *grpc.Server {
	s := grpc.NewServer()

	srv := Server{
		blue:    nil,
		red:     nil,
		lastID:  0,
		current: 0,
		locker:  &sync.Mutex{},

		game:     NewGame(),
		notifier: NewNotifier(),
		plies:    0,
	}
	proto.RegisterBattleshipServer(s, &srv)

	return s
}

func (s *Server) Connect(in *proto.ConnectRequest, stream proto.Battleship_ConnectServer) error {
	if s.blue != nil && s.red != nil {
		return fmt.Errorf("2 clients already connected")
	}

	s.lastID += 1
	c := Client{
		ID:    s.lastID,
		Name:  in.Name,
		leave: make(chan struct{}),
	}

	if err := s.game.SaveDisposition(c.ID, in.Ships); err != nil {
		return err
	}

	if s.blue == nil {
		s.blue = &c
		defer func() {
			s.blue = nil
			log.Println("A Blue has no name")
		}()
		log.Printf("Blue is %s (%d)", c.Name, c.ID)
	} else {
		s.red = &c
		defer func() {
			s.red = nil
			log.Println("A Red has no name")
		}()
		log.Printf("Red is %s (%d)", c.Name, c.ID)
	}

	s.notifier.Register(&c, stream)
	defer s.notifier.Unregister(&c)
	if err := s.notifier.Notify(&c, s.ConnectReplyNotification(&c)); err != nil {
		return err
	}

	if s.blue != nil && s.red != nil {
		s.locker.Lock()
		s.plies = 0
		s.current = s.blue.ID
		s.locker.Unlock()
		log.Printf("Let's begin with %d", s.current)
		s.DispatchGameStatusNotifications()
	}

	<-c.leave
	return nil
}

func (s *Server) Disconnect(ctx context.Context, in *proto.IdMessage) (*proto.EmptyMessage, error) {
	id := in.Id
	var c *Client
	if s.blue != nil && s.blue.ID == id {
		c = s.blue
		s.blue = nil
	} else if s.red != nil && s.red.ID == id {
		c = s.red
		s.red = nil
	} else {
		return nil, fmt.Errorf("Unknown id %d", id)
	}

	s.locker.Lock()
	s.current = -1
	s.locker.Unlock()
	close(c.leave)
	return &proto.EmptyMessage{}, nil
}

func (s *Server) Play(ctx context.Context, in *proto.PlayRequest) (*proto.PlayReply, error) {
	var c *Client
	if s.blue.ID == in.Id {
		c = s.blue
	} else if s.red.ID == in.Id {
		c = s.red
	} else {
		return nil, fmt.Errorf("Unknown id %d", in.Id)
	}

	if in.Id != s.current {
		return &proto.PlayReply{Tile: proto.Tile_UNKNOWN, Status: proto.PlayReply_NOT_YOUR_TURN}, nil
	}

	if in.Position < 0 || in.Position > 100 {
		return &proto.PlayReply{Tile: proto.Tile_UNKNOWN, Status: proto.PlayReply_INVALID_POSITION}, nil
	}

	t := s.game.RegisterPly(c.ID, in.Position)
	if t == proto.Tile_SHIP {
		log.Printf("Player %d touched at %d", c.ID, in.Position)
	} else if t == proto.Tile_SUNK {
		log.Printf("Player %d sank a ship at %d", c.ID, in.Position)
	}

	s.locker.Lock()
	if s.current == s.blue.ID {
		s.current = s.red.ID
	} else {
		s.current = s.blue.ID
	}
	s.plies += 1

	if err := s.DispatchGameStatusNotifications(); err != nil {
		log.Printf("error dispatching game status notification: %v", err)
	}

	w := s.game.Winner()
	if w == s.blue.ID {
		log.Printf("Blue won in %d plies", s.plies)
		s.game = NewGame()
	} else if w == s.red.ID {
		log.Printf("Red won in %d plies", s.plies)
		s.game = NewGame()
	}
	s.locker.Unlock()

	return &proto.PlayReply{Tile: t, Status: proto.PlayReply_ACCEPTED}, nil
}

// ----------------------------------------------------------------------------
// Send notifications

func (s *Server) DispatchGameStatusNotifications() error {
	if err := s.notifier.Notify(s.blue, s.GameStatusNotification(s.blue)); err != nil {
		return err
	}

	if err := s.notifier.Notify(s.red, s.GameStatusNotification(s.red)); err != nil {
		return err
	}

	return nil
}

// ----------------------------------------------------------------------------
// Craft notifications

func (s *Server) ConnectReplyNotification(c *Client) *proto.Notification {
	return &proto.Notification{
		Body: &proto.Notification_ConnectReply{
			ConnectReply: &proto.IdMessage{Id: c.ID},
		},
	}
}

func (s *Server) GameStatusNotification(c *Client) *proto.Notification {
	play := s.current == c.ID
	status := proto.GameStatus_PLAYING
	w := s.game.Winner()
	if c.ID == w {
		status = proto.GameStatus_VICTORY
	} else if w != -1 {
		status = proto.GameStatus_DEFEAT

	}
	return &proto.Notification{
		Body: &proto.Notification_GameStatus{
			GameStatus: &proto.GameStatus{
				Play:   play,
				Status: status,
			},
		},
	}
}
