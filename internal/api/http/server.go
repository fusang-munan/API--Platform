package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/dataplane"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/cache"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
)

type Config struct {
	CookieSecure  bool
	RefreshTTL    time.Duration
	SSEHeartbeat  time.Duration
	AllowedOrigin string
}

type Server struct {
	engine   *gin.Engine
	service  *controlplane.Service
	security *security.Manager
	cache    *cache.Store
	health   HealthDependencies
	config   Config
}

func NewServer(
	service *controlplane.Service,
	securityManager *security.Manager,
	cacheStore *cache.Store,
	gateway *dataplane.Gateway,
	health HealthDependencies,
	cfg Config,
) *Server {
	gin.SetMode(gin.ReleaseMode)
	gin.EnableJsonDecoderDisallowUnknownFields()
	engine := gin.New()
	server := &Server{
		engine: engine, service: service, security: securityManager,
		cache: cacheStore, health: health, config: cfg,
	}
	engine.Use(server.requestContext(), server.recovery(), securityHeaders(), cors(server.config.AllowedOrigin))
	engine.GET("/health/live", server.live)
	engine.GET("/health/ready", server.ready)
	server.registerControlPlane()
	engine.GET("/v1/*path", gin.WrapH(gateway))
	engine.POST("/v1/*path", gin.WrapH(gateway))
	engine.POST("/v1beta/*path", gin.WrapH(gateway))
	engine.POST("/v2/*path", gin.WrapH(gateway))
	return server
}

func (s *Server) Handler() http.Handler {
	return s.engine
}

func (s *Server) registerControlPlane() {
	api := s.engine.Group("/api/v1")
	auth := api.Group("/auth")
	auth.POST("/verification-codes", s.sendVerificationCode)
	auth.POST("/register/student", s.register("student"))
	auth.POST("/login", s.login)
	auth.POST("/session", s.createSession)
	auth.POST("/refresh", s.refresh)
	auth.POST("/logout", s.logout)
	auth.POST("/password/forgot", s.forgotPassword)
	auth.POST("/password/reset", s.resetPassword)
	auth.POST("/teacher-invitations/activate", s.activateTeacher)

	protected := api.Group("")
	protected.Use(s.authenticate())
	protected.GET("/me", s.me)
	protected.GET("/events", s.events)

	student := protected.Group("/student")
	student.Use(requireRole("student"))
	student.Use(s.requireStudentProfile())
	student.GET("/organizations", s.studentOrganizations)
	student.GET("/organizations/:organization_id/projects", s.studentProjects)
	student.GET("/models", s.studentModels)
	student.GET("/common-models", s.studentCommonModels)
	student.GET("/model-catalog", s.studentModelCatalog)
	student.POST("/models", s.addStudentModel)
	student.POST("/models/remove", s.removeStudentModel)
	student.GET("/usage", s.studentUsage)
	student.GET("/usage/calls/:request_id", s.studentCallReceipt)
	student.POST("/api-keys", s.submitAPIKey)
	student.GET("/api-keys", s.studentAPIKeys)
	student.GET("/api-keys/:api_key_id", s.studentAPIKey)
	student.POST("/api-keys/:api_key_id/claim", s.claimAPIKey)
	student.POST("/api-keys/:api_key_id/withdraw", s.withdrawAPIKey)
	student.POST("/api-keys/:api_key_id/regenerate", s.regenerateAPIKey)

	mentor := protected.Group("/mentor")
	mentor.Use(requireRole("mentor"))
	mentor.Use(s.requireMentorProfile())
	mentor.GET("/organizations", s.mentorOrganizations)
	mentor.GET("/organizations/:organization_id/projects", s.mentorProjects)
	mentor.POST("/project-applications", s.applyMentorProject)
	mentor.GET("/project-applications", s.mentorProjectApplications)
	mentor.GET("/api-key-reviews", s.mentorKeyReviews)
	mentor.GET("/api-key-reviews/:api_key_id", s.mentorKeyReview)
	mentor.POST("/api-key-reviews/:api_key_id/approve", s.reviewKeyAsMentor(true))
	mentor.POST("/api-key-reviews/:api_key_id/reject", s.reviewKeyAsMentor(false))
	mentor.GET("/api-keys", s.mentorAPIKeys)
	mentor.GET("/api-keys/:api_key_id", s.mentorAPIKey)
	mentor.GET("/projects/:project_id/api-keys", s.mentorActiveKeys)
	mentor.POST("/api-keys/:api_key_id/deactivate", s.deactivateKeyAsMentor)
	mentor.POST("/api-keys/:api_key_id/activate", s.activateKeyAsMentor)
	mentor.GET("/projects/:project_id/usage", s.mentorProjectUsage)
	mentor.PATCH("/api-keys/:api_key_id/monthly-credit-quota", s.updateMentorKeyQuota)
	mentor.GET("/call-logs", s.mentorCallLogs)
	mentor.GET("/call-logs/:call_id", s.mentorCallLog)
	mentor.GET("/call-turns", s.mentorCallTurns)
	mentor.GET("/call-turns/:turn_id", s.mentorCallTurn)
	mentor.GET("/input-monitor", s.mentorInputs)
	mentor.GET("/input-monitor/:call_id", s.mentorInput)

	teacher := protected.Group("/teacher")
	teacher.Use(requireRole("teacher"))
	teacher.POST("/invitations", s.inviteTeacher)
	teacher.GET("/students", s.teacherStudents)
	teacher.POST("/students/:student_id/mentor-profile", s.grantMentorProfile)
	teacher.POST("/students/:student_id/mentor-profile/revoke", s.revokeMentorProfile)
	teacher.GET("/organizations", s.teacherOrganizations)
	teacher.POST("/organizations", s.createOrganization)
	teacher.PATCH("/organizations/:organization_id", s.updateOrganization)
	teacher.GET("/projects", s.teacherProjects)
	teacher.POST("/projects", s.createProject)
	teacher.PATCH("/projects/:project_id", s.updateProject)
	teacher.GET("/projects/:project_id/mentor-candidates", s.projectMentorCandidates)
	teacher.POST("/projects/:project_id/mentors/:mentor_id", s.assignProjectMentor)
	teacher.DELETE("/projects/:project_id/mentors/:mentor_id", s.removeProjectMentor)
	teacher.GET("/project-budgets", s.teacherProjectBudgets)
	teacher.PATCH("/project-budgets/:project_id", s.updateProjectBudget)
	teacher.GET("/projects/:project_id/usage", s.teacherProjectUsage)
	teacher.PATCH("/projects/:project_id/members/:user_id/monthly-credit-quota", s.updateTeacherMemberQuota)
	teacher.GET("/project-spend", s.teacherProjectSpend)
	teacher.GET("/mentor-project-applications", s.teacherMentorApplications)
	teacher.POST("/mentor-project-applications/:application_id/approve", s.reviewMentorApplication(true))
	teacher.POST("/mentor-project-applications/:application_id/reject", s.reviewMentorApplication(false))
	teacher.GET("/providers", s.teacherProviders)
	teacher.POST("/providers", s.createProvider)
	teacher.GET("/providers/:provider_id", s.teacherProvider)
	teacher.PATCH("/providers/:provider_id", s.updateProvider)
	teacher.DELETE("/providers/:provider_id", s.deleteProvider)
	teacher.GET("/providers/:provider_id/credentials", s.providerCredentials)
	teacher.POST("/providers/:provider_id/credentials", s.createProviderCredential)
	teacher.PATCH("/providers/:provider_id/credentials/:credential_id", s.updateProviderCredential)
	teacher.DELETE("/providers/:provider_id/credentials/:credential_id", s.deleteProviderCredential)
	teacher.POST("/providers/:provider_id/reset-catalog", s.resetProviderCatalog)
	teacher.GET("/providers/:provider_id/beichen", s.beichenOverview)
	teacher.POST("/providers/:provider_id/beichen/sync", s.syncBeichen)
	teacher.POST("/providers/:provider_id/beichen/models/enable", s.enableBeichenModel)
	teacher.GET("/providers/:provider_id/beichen/access/:credential_id", s.beichenPanelAccess)
	teacher.PATCH("/providers/:provider_id/beichen/access/:credential_id", s.beichenPanelAccess)
	teacher.PATCH("/providers/:provider_id/beichen/price", s.setBeichenPrice)
	teacher.POST("/providers/:provider_id/beichen/import", s.importBeichen)
	teacher.GET("/provider-catalog-candidates", s.teacherProviderCatalogCandidates)
	teacher.GET("/models", s.teacherModels)
	teacher.POST("/models", s.createModel)
	teacher.GET("/models/detail", s.teacherModel)
	teacher.PATCH("/models/configuration", s.updateModel)
	teacher.POST("/model-bindings", s.createBinding)
	teacher.POST("/model-binding-route-probes", s.probeModelBindingRoute)
	teacher.POST("/model-probes", s.probeModel)
	teacher.PATCH("/model-bindings/:binding_id", s.updateBinding)
}
