// Package windsurf 实现 Windsurf 本地 LS 的 gRPC service 访问。
//
// 请求 / 响应的 wire format 与 Node 版 WindsurfAPI/src/windsurf.js 完全一致；
// 每个 builder / parser 的 field number 都在注释里回指 Node 行号，便于对拍。
package windsurf

const (
	svcPrefix = "/exa.language_server_pb.LanguageServerService/"

	// 用到的全部 LS gRPC path
	PathGetUserStatus                   = svcPrefix + "GetUserStatus"
	PathInitializeCascadePanelState     = svcPrefix + "InitializeCascadePanelState"
	PathAddTrackedWorkspace             = svcPrefix + "AddTrackedWorkspace"
	PathUpdateCascadeWorkspaceTrust     = svcPrefix + "UpdateWorkspaceTrust"
	PathUpdatePanelStateWithUserStatus  = svcPrefix + "UpdatePanelStateWithUserStatus"
	PathStartCascade                    = svcPrefix + "StartCascade"
	PathSendUserCascadeMessage          = svcPrefix + "SendUserCascadeMessage"
	PathGetCascadeTrajectorySteps       = svcPrefix + "GetCascadeTrajectorySteps"
	PathGetCascadeTrajectoryGeneratorMD = svcPrefix + "GetCascadeTrajectoryGeneratorMetadata"
	PathHeartbeat                       = svcPrefix + "Heartbeat"
)
