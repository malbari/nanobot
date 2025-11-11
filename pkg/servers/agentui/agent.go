package agentui

import (
	"context"
	"time"

	"github.com/nanobot-ai/nanobot/pkg/log"
	"github.com/nanobot-ai/nanobot/pkg/mcp"
	pkgsession "github.com/nanobot-ai/nanobot/pkg/session"
	"github.com/nanobot-ai/nanobot/pkg/sessiondata"
	"github.com/nanobot-ai/nanobot/pkg/tools"
	"github.com/nanobot-ai/nanobot/pkg/types"
	"github.com/nanobot-ai/nanobot/pkg/version"
)

type Server struct {
	tools   mcp.ServerTools
	data    *sessiondata.Data
	runtime Caller
}

type Caller interface {
	Call(ctx context.Context, server, tool string, args any, opts ...tools.CallOptions) (ret *types.CallResult, err error)
	GetClient(ctx context.Context, name string) (*mcp.Client, error)
}

func NewServer(d *sessiondata.Data, r Caller) *Server {
	s := &Server{
		data:    d,
		runtime: r,
	}

	s.tools = mcp.NewServerTools(
		setCurrentAgentCall{s: s},
		chatCall{s: s},
	)

	return s
}

func (s *Server) OnMessage(ctx context.Context, msg mcp.Message) {
	switch msg.Method {
	case "initialize":
		mcp.Invoke(ctx, msg, s.initialize)
	case "notifications/initialized":
		// nothing to do
	case "tools/list":
		mcp.Invoke(ctx, msg, s.tools.List)
	case "tools/call":
		mcp.Invoke(ctx, msg, s.tools.Call)
	default:
		msg.SendError(ctx, mcp.ErrRPCMethodNotFound.WithMessage(msg.Method))
	}
}
func (s *Server) describeSession(ctx context.Context, args any) <-chan struct{} {
	result := make(chan struct{})
	var description string

	session := mcp.SessionFromContext(ctx)
	session = session.Parent
	session.Get(types.DescriptionSessionKey, &description)
	if description == "" {
		go func() {
			ret, err := s.runtime.Call(ctx, "nanobot.summary", "nanobot.summary", args)
			if err != nil {
				log.Errorf(ctx, "Failed to generate title: %v", err)
				close(result)
				return
			}
			for _, content := range ret.Content {
				if content.Type == "text" {
					description = content.Text
					log.Infof(ctx, "Generated title: %q", description)
					session.Set(types.DescriptionSessionKey, description)
					
					// Update database with the new description
					var manager pkgsession.Manager
					if session.Get(pkgsession.ManagerSessionKey, &manager) {
						log.Infof(ctx, "Found manager in session")
						// Get session ID from the parent session state
						state, err := session.State()
						if err == nil && state != nil {
							sessionID := state.ID
							log.Infof(ctx, "Session ID: %s", sessionID)
							if sessionID != "" {
								dbSession, err := manager.DB.Get(ctx, sessionID)
								if err != nil {
									log.Errorf(ctx, "Failed to get session from DB: %v", err)
								} else if dbSession != nil {
									log.Infof(ctx, "Updating DB session description to: %q", description)
									dbSession.Description = description
									if err := manager.DB.Update(ctx, dbSession); err != nil {
										log.Errorf(ctx, "Failed to update session in DB: %v", err)
									} else {
										log.Infof(ctx, "Successfully updated session description in DB")
										// Small delay to ensure DB commit is complete
										time.Sleep(200 * time.Millisecond)
									}
								}
							}
						} else {
							log.Errorf(ctx, "Failed to get session state: %v", err)
						}
					} else {
						log.Infof(ctx, "Manager not found in session")
					}
					break
				}
			}
			// Close channel only after DB is updated
			close(result)
		}()
	} else {
		close(result)
	}

	return result
}

func (s *Server) initialize(ctx context.Context, _ mcp.Message, params mcp.InitializeRequest) (*mcp.InitializeResult, error) {
	//target, err := s.data.GetCurrentAgentTargetMapping(ctx)
	//if err != nil {
	//	return nil, err
	//}
	//
	//remoteClient, err := s.runtime.GetClient(ctx, target.MCPServer)
	//if err != nil {
	//	return nil, err
	//}
	//
	//tools, err := remoteClient.ListTools(ctx)
	//if err != nil {
	//	return nil, fmt.Errorf("failed to list tools: %w", err)
	//}
	//
	//found := false
	//for _, tool := range tools.Tools {
	//	if tool.Name == "set_current_agent" {
	//		found = true
	//		break
	//	}
	//}
	//
	//if !found {
	//	delete(s.tools, "set_current_agent")
	//}
	//
	return &mcp.InitializeResult{
		ProtocolVersion: params.ProtocolVersion,
		Capabilities: mcp.ServerCapabilities{
			Tools: &mcp.ToolsServerCapability{},
		},
		ServerInfo: mcp.ServerInfo{
			Name:    version.Name,
			Version: version.Get().String(),
		},
	}, nil
}
