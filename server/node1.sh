# build tag so our future Redis cluster code is compiled
export GO_BUILD_TAGS="oss_cluster"

# override any paths that need to be unique per node
export MM_FILESETTINGS_DIRECTORY="/tmp/mmdata1"
export MM_SERVICESETTINGS_LISTENADDRESS=":8065"
export MM_SQLSETTINGS_DATASOURCE="postgres://mmuser:mostest@localhost/mattermost_test?sslmode=disable"
export MM_CACHESETTINGS_REDISADDRESS="localhost:6379"
export MM_CLUSTERSETTINGS_ENABLE="true"

make run-server
