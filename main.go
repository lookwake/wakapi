package main

import (
	"embed"
	"flag"
	"github.com/duke-git/lancet/v2/condition"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/emvi/logbuch"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/lpar/gzipped/v2"
	httpSwagger "github.com/swaggo/http-swagger"
	_ "gorm.io/driver/mysql"
	_ "gorm.io/driver/postgres"
	_ "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	conf "github.com/muety/wakapi/config"
	"github.com/muety/wakapi/middlewares"
	"github.com/muety/wakapi/migrations"
	"github.com/muety/wakapi/repositories"
	"github.com/muety/wakapi/routes"
	"github.com/muety/wakapi/routes/api"
	shieldsV1Routes "github.com/muety/wakapi/routes/compat/shields/v1"
	wtV1Routes "github.com/muety/wakapi/routes/compat/wakatime/v1"
	"github.com/muety/wakapi/routes/relay"
	"github.com/muety/wakapi/services"
	"github.com/muety/wakapi/services/mail"
	"github.com/muety/wakapi/static/docs"
	fsutils "github.com/muety/wakapi/utils/fs"

	_ "net/http/pprof"
)

// Embed version.txt
//
//go:embed version.txt
var version string

// Embed static files
//
//go:embed static
var staticFiles embed.FS

var (
	db     *gorm.DB
	config *conf.Config
)

var (
	aliasRepository           repositories.IAliasRepository
	heartbeatRepository       repositories.IHeartbeatRepository
	userRepository            repositories.IUserRepository
	languageMappingRepository repositories.ILanguageMappingRepository
	projectLabelRepository    repositories.IProjectLabelRepository
	summaryRepository         repositories.ISummaryRepository
	leaderboardRepository     *repositories.LeaderboardRepository
	keyValueRepository        repositories.IKeyValueRepository
	diagnosticsRepository     repositories.IDiagnosticsRepository
	metricsRepository         *repositories.MetricsRepository
)

var (
	aliasService           services.IAliasService
	heartbeatService       services.IHeartbeatService
	userService            services.IUserService
	languageMappingService services.ILanguageMappingService
	projectLabelService    services.IProjectLabelService
	durationService        services.IDurationService
	summaryService         services.ISummaryService
	leaderboardService     services.ILeaderboardService
	aggregationService     services.IAggregationService
	mailService            services.IMailService
	keyValueService        services.IKeyValueService
	reportService          services.IReportService
	activityService        services.IActivityService
	diagnosticsService     services.IDiagnosticsService
	housekeepingService    services.IHousekeepingService
	miscService            services.IMiscService
)

// TODO: Refactor entire project to be structured after business domains

// @title Wakapi API
// @version 1.0
// @description REST API to interact with [Wakapi](https://wakapi.dev)
// @description
// @description ## Authentication
// @description Set header `Authorization` to your API Key encoded as Base64 and prefixed with `Basic`
// @description **Example:** `Basic ODY2NDhkNzQtMTljNS00NTJiLWJhMDEtZmIzZWM3MGQ0YzJmCg==`

// @contact.name Ferdinand Mütsch
// @contact.url https://github.com/muety
// @contact.email ferdinand@muetsch.io

// @license.name GPL-3.0
// @license.url https://github.com/muety/wakapi/blob/master/LICENSE

// @securitydefinitions.apikey ApiKeyAuth
// @in header
// @name Authorization

func main() {
	var versionFlag = flag.Bool("version", false, "print version")
	var configFlag = flag.String("config", conf.DefaultConfigPath, "config file location")
	flag.Parse()

	if *versionFlag {
		print(version)
		os.Exit(0)
	}
	config = conf.Load(*configFlag, version)

	// Configure Swagger docs
	docs.SwaggerInfo.BasePath = config.Server.BasePath + "/api"

	// Set log level
	if config.IsDev() {
		logbuch.SetLevel(logbuch.LevelDebug)
	} else {
		logbuch.SetLevel(logbuch.LevelInfo)
	}
	logbuch.Info("Wakapi " + version)

	// Set up GORM
	gormLogger := logger.New(
		log.New(os.Stdout, "", log.LstdFlags),
		logger.Config{
			SlowThreshold: time.Minute,
			Colorful:      false,
			LogLevel:      logger.Silent,
		},
	)

	// Connect to database
	var err error
	logbuch.Info("starting with %s database", config.Db.Dialect)
	db, err = gorm.Open(config.Db.GetDialector(), &gorm.Config{Logger: gormLogger}, conf.GetWakapiDBOpts(&config.Db))
	if err != nil {
		logbuch.Error(err.Error())
		logbuch.Fatal("could not open database")
	}

	if config.IsDev() {
		db = db.Debug()
	}
	sqlDb, err := db.DB()
	if err != nil {
		logbuch.Error(err.Error())
		logbuch.Fatal("could not connect to database")
	}
	sqlDb.SetMaxIdleConns(int(config.Db.MaxConn))
	sqlDb.SetMaxOpenConns(int(config.Db.MaxConn))
	defer sqlDb.Close()

	// Migrate database schema
	if !config.SkipMigrations {
		migrations.Run(db, config)
	}

	// Repositories
	aliasRepository = repositories.NewAliasRepository(db)
	heartbeatRepository = repositories.NewHeartbeatRepository(db)
	userRepository = repositories.NewUserRepository(db)
	languageMappingRepository = repositories.NewLanguageMappingRepository(db)
	projectLabelRepository = repositories.NewProjectLabelRepository(db)
	summaryRepository = repositories.NewSummaryRepository(db)
	leaderboardRepository = repositories.NewLeaderboardRepository(db)
	keyValueRepository = repositories.NewKeyValueRepository(db)
	diagnosticsRepository = repositories.NewDiagnosticsRepository(db)
	metricsRepository = repositories.NewMetricsRepository(db)

	// Services
	mailService = mail.NewMailService()
	aliasService = services.NewAliasService(aliasRepository)
	userService = services.NewUserService(mailService, userRepository)
	languageMappingService = services.NewLanguageMappingService(languageMappingRepository)
	projectLabelService = services.NewProjectLabelService(projectLabelRepository)
	heartbeatService = services.NewHeartbeatService(heartbeatRepository, languageMappingService)
	durationService = services.NewDurationService(heartbeatService)
	summaryService = services.NewSummaryService(summaryRepository, durationService, aliasService, projectLabelService)
	aggregationService = services.NewAggregationService(userService, summaryService, heartbeatService)
	keyValueService = services.NewKeyValueService(keyValueRepository)
	reportService = services.NewReportService(summaryService, userService, mailService)
	activityService = services.NewActivityService(summaryService)
	diagnosticsService = services.NewDiagnosticsService(diagnosticsRepository)
	housekeepingService = services.NewHousekeepingService(userService, heartbeatService, summaryService)
	miscService = services.NewMiscService(userService, heartbeatService, summaryService, keyValueService, mailService)

	if config.App.LeaderboardEnabled {
		leaderboardService = services.NewLeaderboardService(leaderboardRepository, summaryService, userService)
	}

	// Schedule background tasks
	go conf.StartJobs()
	go aggregationService.Schedule()
	go reportService.Schedule()
	go housekeepingService.Schedule()
	go miscService.Schedule()

	if config.App.LeaderboardEnabled {
		go leaderboardService.Schedule()
	}

	routes.Init()

	// API Handlers
	healthApiHandler := api.NewHealthApiHandler(db)
	heartbeatApiHandler := api.NewHeartbeatApiHandler(userService, heartbeatService, languageMappingService)
	summaryApiHandler := api.NewSummaryApiHandler(userService, summaryService)
	metricsHandler := api.NewMetricsHandler(userService, summaryService, heartbeatService, leaderboardService, keyValueService, metricsRepository)
	diagnosticsHandler := api.NewDiagnosticsApiHandler(userService, diagnosticsService)
	avatarHandler := api.NewAvatarHandler()
	activityHandler := api.NewActivityApiHandler(userService, activityService)
	badgeHandler := api.NewBadgeHandler(userService, summaryService)

	// Compat Handlers
	wakatimeV1StatusBarHandler := wtV1Routes.NewStatusBarHandler(userService, summaryService)
	wakatimeV1AllHandler := wtV1Routes.NewAllTimeHandler(userService, summaryService)
	wakatimeV1SummariesHandler := wtV1Routes.NewSummariesHandler(userService, summaryService)
	wakatimeV1StatsHandler := wtV1Routes.NewStatsHandler(userService, summaryService)
	wakatimeV1UsersHandler := wtV1Routes.NewUsersHandler(userService, heartbeatService)
	wakatimeV1ProjectsHandler := wtV1Routes.NewProjectsHandler(userService, heartbeatService)
	wakatimeV1HeartbeatsHandler := wtV1Routes.NewHeartbeatHandler(userService, heartbeatService)
	wakatimeV1LeadersHandler := wtV1Routes.NewLeadersHandler(userService, leaderboardService)
	shieldV1BadgeHandler := shieldsV1Routes.NewBadgeHandler(summaryService, userService)

	// MVC Handlers
	summaryHandler := routes.NewSummaryHandler(summaryService, userService, keyValueService)
	settingsHandler := routes.NewSettingsHandler(userService, heartbeatService, summaryService, aliasService, aggregationService, languageMappingService, projectLabelService, keyValueService, mailService)
	subscriptionHandler := routes.NewSubscriptionHandler(userService, mailService, keyValueService)
	projectsHandler := routes.NewProjectsHandler(userService, heartbeatService)
	homeHandler := routes.NewHomeHandler(keyValueService)
	loginHandler := routes.NewLoginHandler(userService, mailService)
	imprintHandler := routes.NewImprintHandler(keyValueService)
	leaderboardHandler := condition.TernaryOperator[bool, routes.Handler](config.App.LeaderboardEnabled, routes.NewLeaderboardHandler(userService, leaderboardService), routes.NewNoopHandler())

	// Other Handlers
	relayHandler := relay.NewRelayHandler()

	// Setup Routing
	router := chi.NewRouter()
	router.Use(
		middleware.CleanPath,
		middleware.StripSlashes,
		middleware.Recoverer,
		middlewares.NewPrincipalMiddleware(),
		middlewares.NewLoggingMiddleware(logbuch.Info, []string{
			"/assets",
			"/favicon",
			"/service-worker.js",
			"/api/health",
			"/api/avatar",
		}),
	)
	if config.Sentry.Dsn != "" {
		router.Use(middlewares.NewSentryMiddleware())
	}

	// Setup Sub Routers
	rootRouter := chi.NewRouter()
	rootRouter.Use(middlewares.NewSecurityMiddleware())

	apiRouter := chi.NewRouter()

	// Hook sub routers
	router.Mount("/", rootRouter)
	router.Mount("/api", apiRouter)

	// Route registrations
	homeHandler.RegisterRoutes(rootRouter)
	loginHandler.RegisterRoutes(rootRouter)
	imprintHandler.RegisterRoutes(rootRouter)
	summaryHandler.RegisterRoutes(rootRouter)
	leaderboardHandler.RegisterRoutes(rootRouter)
	projectsHandler.RegisterRoutes(rootRouter)
	settingsHandler.RegisterRoutes(rootRouter)
	subscriptionHandler.RegisterRoutes(rootRouter)
	relayHandler.RegisterRoutes(rootRouter)

	// API route registrations
	summaryApiHandler.RegisterRoutes(apiRouter)
	healthApiHandler.RegisterRoutes(apiRouter)
	heartbeatApiHandler.RegisterRoutes(apiRouter)
	metricsHandler.RegisterRoutes(apiRouter)
	diagnosticsHandler.RegisterRoutes(apiRouter)
	avatarHandler.RegisterRoutes(apiRouter)
	activityHandler.RegisterRoutes(apiRouter)
	badgeHandler.RegisterRoutes(apiRouter)
	wakatimeV1StatusBarHandler.RegisterRoutes(apiRouter)
	wakatimeV1AllHandler.RegisterRoutes(apiRouter)
	wakatimeV1SummariesHandler.RegisterRoutes(apiRouter)
	wakatimeV1StatsHandler.RegisterRoutes(apiRouter)
	wakatimeV1UsersHandler.RegisterRoutes(apiRouter)
	wakatimeV1ProjectsHandler.RegisterRoutes(apiRouter)
	wakatimeV1HeartbeatsHandler.RegisterRoutes(apiRouter)
	wakatimeV1LeadersHandler.RegisterRoutes(apiRouter)
	shieldV1BadgeHandler.RegisterRoutes(apiRouter)

	// Static Routes
	// https://github.com/golang/go/issues/43431
	embeddedStatic, _ := fs.Sub(staticFiles, "static")
	static := conf.ChooseFS("static", embeddedStatic)

	assetsStaticFs := fsutils.NewExistsHttpFS(fsutils.NewExistsFS(static).WithCache(!config.IsDev()))
	assetsFileServer := http.FileServer(assetsStaticFs)
	if !config.IsDev() {
		assetsFileServer = gzipped.FileServer(assetsStaticFs)
	}
	staticFileServer := http.FileServer(http.FS(fsutils.NeuteredFileSystem{FS: static}))

	router.Get("/contribute.json", staticFileServer.ServeHTTP)
	router.Get("/assets/*", assetsFileServer.ServeHTTP)
	router.Get("/swagger-ui", http.RedirectHandler("swagger-ui/", http.StatusMovedPermanently).ServeHTTP) // https://github.com/swaggo/http-swagger/issues/44
	router.Get("/swagger-ui/*", httpSwagger.WrapHandler)

	if config.EnablePprof {
		logbuch.Info("profiling enabled, exposing pprof data at http://127.0.0.1:6060/debug/pprof")
		go func() {
			_ = http.ListenAndServe("127.0.0.1:6060", nil)
		}()
	}

	go func() {
		for {
			time.Sleep(1 * time.Minute)
			runtime.GC()
			debug.FreeOSMemory()
		}
	}()

	// Listen HTTP
	listen(router)
}

func listen(handler http.Handler) {
	var s4, s6, sSocket *http.Server

	// IPv4
	if config.Server.ListenIpV4 != "-" && config.Server.ListenIpV4 != "" {
		bindString4 := config.Server.ListenIpV4 + ":" + strconv.Itoa(config.Server.Port)
		s4 = &http.Server{
			Handler:      handler,
			Addr:         bindString4,
			ReadTimeout:  time.Duration(config.Server.TimeoutSec) * time.Second,
			WriteTimeout: time.Duration(config.Server.TimeoutSec) * time.Second,
		}
	}

	// IPv6
	if config.Server.ListenIpV6 != "-" && config.Server.ListenIpV6 != "" {
		bindString6 := "[" + config.Server.ListenIpV6 + "]:" + strconv.Itoa(config.Server.Port)
		s6 = &http.Server{
			Handler:      handler,
			Addr:         bindString6,
			ReadTimeout:  time.Duration(config.Server.TimeoutSec) * time.Second,
			WriteTimeout: time.Duration(config.Server.TimeoutSec) * time.Second,
		}
	}

	// UNIX domain socket
	if config.Server.ListenSocket != "-" && config.Server.ListenSocket != "" {
		// Remove if exists
		if _, err := os.Stat(config.Server.ListenSocket); err == nil {
			logbuch.Info("👉 Removing unix socket %s", config.Server.ListenSocket)
			if err := os.Remove(config.Server.ListenSocket); err != nil {
				logbuch.Fatal(err.Error())
			}
		}
		sSocket = &http.Server{
			Handler:      handler,
			ReadTimeout:  time.Duration(config.Server.TimeoutSec) * time.Second,
			WriteTimeout: time.Duration(config.Server.TimeoutSec) * time.Second,
		}
	}

	if config.UseTLS() {
		if s4 != nil {
			logbuch.Info("👉 Listening for HTTPS on %s... ✅", s4.Addr)
			go func() {
				if err := s4.ListenAndServeTLS(config.Server.TlsCertPath, config.Server.TlsKeyPath); err != nil {
					logbuch.Fatal(err.Error())
				}
			}()
		}
		if s6 != nil {
			logbuch.Info("👉 Listening for HTTPS on %s... ✅", s6.Addr)
			go func() {
				if err := s6.ListenAndServeTLS(config.Server.TlsCertPath, config.Server.TlsKeyPath); err != nil {
					logbuch.Fatal(err.Error())
				}
			}()
		}
		if sSocket != nil {
			logbuch.Info("👉 Listening for HTTPS on %s... ✅", config.Server.ListenSocket)
			go func() {
				unixListener, err := net.Listen("unix", config.Server.ListenSocket)
				if err != nil {
					logbuch.Fatal(err.Error())
				}
				if err := os.Chmod(config.Server.ListenSocket, os.FileMode(config.Server.ListenSocketMode)); err != nil {
					logbuch.Warn("failed to set user permissions for unix socket, %v", err)
				}
				if err := sSocket.ServeTLS(unixListener, config.Server.TlsCertPath, config.Server.TlsKeyPath); err != nil {
					logbuch.Fatal(err.Error())
				}
			}()
		}
	} else {
		if s4 != nil {
			logbuch.Info("👉 Listening for HTTP on %s... ✅", s4.Addr)
			go func() {
				if err := s4.ListenAndServe(); err != nil {
					logbuch.Fatal(err.Error())
				}
			}()
		}
		if s6 != nil {
			logbuch.Info("👉 Listening for HTTP on %s... ✅", s6.Addr)
			go func() {
				if err := s6.ListenAndServe(); err != nil {
					logbuch.Fatal(err.Error())
				}
			}()
		}
		if sSocket != nil {
			logbuch.Info("👉 Listening for HTTP on %s... ✅", config.Server.ListenSocket)
			go func() {
				unixListener, err := net.Listen("unix", config.Server.ListenSocket)
				if err != nil {
					logbuch.Fatal(err.Error())
				}
				if err := os.Chmod(config.Server.ListenSocket, os.FileMode(config.Server.ListenSocketMode)); err != nil {
					logbuch.Warn("failed to set user permissions for unix socket, %v", err)
				}
				if err := sSocket.Serve(unixListener); err != nil {
					logbuch.Fatal(err.Error())
				}
			}()
		}
	}

	<-make(chan interface{}, 1)
}
