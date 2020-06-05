# 基于 Go + MySQL + ES 实现一个 Tag API 服务

Tag 是一个很常见的功能，这篇文章将使用 Go + MySQL + ES 实现一个 500 多行的 tag API 服务，支持 创建/搜索 标签、标签关联到实体 和 查询实体所关联的标签列表。

目录：

[toc]

## 初始化环境

### MySQL

```
brew install mysql
```

### ES

这里直接通过 docker 来启动 ES:

```
docker run -d --name elasticsearch -p 9200:9200 -p 9300:9300 -e "discovery.type=single-node" elasticsearch
```

启动后可以通过 curl 检查是否已经启动和获取版本信息：

```
curl localhost:9200
{
  "name" : "5059f2c85a1d",
  "cluster_name" : "docker-cluster",
  "cluster_uuid" : "T5EjufvlSdCcZXVDJFi2cA",
  "version" : {
    "number" : "7.7.1",
    "build_flavor" : "default",
    "build_type" : "docker",
    "build_hash" : "ad56dce891c901a492bb1ee393f12dfff473a423",
    "build_date" : "2020-05-28T16:30:01.040088Z",
    "build_snapshot" : false,
    "lucene_version" : "8.5.1",
    "minimum_wire_compatibility_version" : "6.8.0",
    "minimum_index_compatibility_version" : "6.0.0-beta1"
  },
  "tagline" : "You Know, for Search"
}
```

注意上面的部署**仅用于开发环境**，如果需要在生产部署通过 docker 部署，请参考官方文档: [Install Elasticsearch with Docker](https://www.elastic.co/guide/en/elasticsearch/reference/7.5/docker.html)。

## 设计存储结构

先在 MySQL 里面创建一个 test 数据库:

```mysql
create database test;
use test;
```

创建 tag_tbl 表:

```mysql
CREATE TABLE `tag_tbl` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `name` varchar(40) NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `name` (`name`) USING HASH
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

tag_tbl 用于存储标签，注意这里给我们给 name 字段加上了一个唯一键，并使用 hash 作为索引方法，关于 hash 索引，可以参考官方文档：[Comparison of B-Tree and Hash Indexes](https://dev.mysql.com/doc/refman/8.0/en/index-btree-hash.html#hash-index-characteristics)。

再创建 entity_tag_tbl 用于存储实体关联的 tag:

```mysql
CREATE TABLE `entity_tag_tbl` (
  `id` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `entity_id` int(10) unsigned NOT NULL,
  `tag_id` int(10) unsigned NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `entity_id` (`entity_id`,`tag_id`) USING BTREE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

## 设计 API

### 创建标签

Request:

```
POST /api/tag
{
    "name": "your tag name"
}
```

Response:

```
{
    "tag_id": 1
}
```

### 搜索标签

Request:

```
GET /api/tag/search
{
    "keyword": "cat"
}
```

Response:

```
{
    "matchs": [
        {
            "tag_id": 5,
            "name": "cat"
        },
        {
            "tag_id": 6,
            "name": "cat pictures"
        }
    ]
}
```

### 关联标签到实体

Request:

```
POST /api/tag/link_entity
{
    "entity_id": 1,
    "tag_id": 3
}
```

Response:

```json
{
    "link_id": 1
}
```

### 查询实体关联的标签列表

Request:

```
GET /api/tag/entity_tags
{
    "entity_id": 1
}
```

Response:

```json
{
    "tags": [
        {
            "tag_id": 3,
            "name": "美食"
        }
    ]
}
```

## 编码实现

初始化：

```
mkdir tag-server
cd tag-server
go mod init github.com/3vilive/tag-server
```

安装将要用到依赖项：

```
go get github.com/go-sql-driver/mysql github.com/jmoiron/sqlx github.com/gin-gonic/gin github.com/elastic/go-elasticsearch/v7
```

创建 cmd/api-server/main.go 并编写脚手架代码:

```go
package main

import (
    "net/http"

    "github.com/gin-gonic/gin"
)

func OnNewTag(c *gin.Context) {
    c.JSON(http.StatusOK, gin.H{
        "tag_id": 0,
    })
}

func OnSearchTag(c *gin.Context) {
    c.JSON(http.StatusOK, gin.H{
        "matches": []struct{}{},
    })
}

func OnLinkEntity(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"link_id": 0,
	})
}

func OnEntityTags(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"tags": []struct{}{},
	})
	return
}

func main() {
    r := gin.Default()

    r.POST("/api/tag", OnNewTag)
    r.GET("/api/tag/search", OnSearchTag)
    r.POST("/api/tag/link_entity", OnLinkEntity)
    r.GET("/api/tag/entity_tags", OnEntityTags)

    r.Run(":9800")
}
```

### 实现创建标签的 API

连接数据库：

```go
import "github.com/jmoiron/sqlx"
import _ "github.com/go-sql-driver/mysql" // mysql driver

var (
    mysqlDB *sqlx.DB
)

func init() {
    mysqlDB = sqlx.MustOpen("mysql", "test:test@tcp(localhost:3306)/test?parseTime=True&loc=Local&multiStatements=true&charset=utf8mb4")
}
```

定义 Tag 结构：

```go
type Tag struct {
    TagID int    `db:"id"`
    Name  string `db:"name"`
}
```

编写创建标签的逻辑：

```go
// NewTagReqBody 创建标签的请求体
type NewTagReqBody struct {
    Name string `json:"name"`
}

// OnNewTag 创建标签
func OnNewTag(c *gin.Context) {
    var reqBody NewTagReqBody
    if bindErr := c.BindJSON(&reqBody); bindErr != nil {
        c.JSON(http.StatusBadRequest, gin.H{
            "status":  http.StatusBadRequest,
            "message": bindErr.Error(),
        })
        return
    }

    // 判断传入的 tag 名称是否为空
    tagName := strings.TrimSpace(reqBody.Name)
    if tagName == "" {
        c.JSON(http.StatusBadRequest, gin.H{
            "status":  http.StatusBadRequest,
            "message": "invalid name",
        })
        return
    }

    var queryTag Tag
    queryErr := mysqlDB.Get(&queryTag, "select id, name from tag_tbl where name = ?", tagName)
    if queryErr == nil {
        // tag 已经存在
        c.JSON(http.StatusOK, gin.H{
            "tag_id": queryTag.TagID,
        })
        return
    }

    // 查询 mysql 出现错误
    if queryErr != nil && queryErr != sql.ErrNoRows {
        c.JSON(http.StatusInternalServerError, gin.H{
            "status":  http.StatusInternalServerError,
            "message": queryErr.Error(),
        })
        return
    }

    // tag 不存在，创建 tag
    result, execErr := mysqlDB.Exec("insert into tag_tbl (name) values (?) on duplicate key update created_at = now()", tagName)
    if execErr != nil {
        c.JSON(http.StatusInternalServerError, gin.H{
            "status":  http.StatusInternalServerError,
            "message": execErr.Error(),
        })
        return
    }

    tagID, err := result.LastInsertId()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{
            "status":  http.StatusInternalServerError,
            "message": err.Error(),
        })
        return
    }

    c.JSON(http.StatusOK, gin.H{
        "tag_id": tagID,
    })
}
```

启动测试一下：

```
go run cmd/api-server/main.go
[GIN-debug] [WARNING] Creating an Engine instance with the Logger and Recovery middleware already attached.

[GIN-debug] [WARNING] Running in "debug" mode. Switch to "release" mode in production.
 - using env:   export GIN_MODE=release
 - using code:  gin.SetMode(gin.ReleaseMode)

[GIN-debug] POST   /api/tag                  --> main.OnNewTag (3 handlers)
[GIN-debug] POST   /api/tag/search           --> main.OnSearchTag (3 handlers)
[GIN-debug] Listening and serving HTTP on :9800
```


创建一个名为 test 的标签:

```
curl --request POST \
  --url http://localhost:9800/api/tag \
  --header 'content-type: application/json' \
  --data '{
    "name": "test"
}'
```

响应：

```
{
  "tag_id": 1
}
```

再创建一个叫做 测试 的标签：

```
curl --request POST \
  --url http://localhost:9800/api/tag \
  --header 'content-type: application/json' \
  --data '{
    "name": "测试"
}'
```

响应：

```
{
  "tag_id": 2
}
```

重新运行一遍创建 test 标签的请求：

```
curl --request POST \
  --url http://localhost:9800/api/tag \
  --header 'content-type: application/json' \
  --data '{
    "name": "test"
}'
```

响应：

```
{
  "tag_id": 1
}
```

测试结果符合预期，当前完整文件内容如下：

```go
package main

import (
    "database/sql"
    "net/http"
    "strings"

    "github.com/gin-gonic/gin"
    "github.com/jmoiron/sqlx"

    _ "github.com/go-sql-driver/mysql" // mysql driver
)

var (
    mysqlDB *sqlx.DB
)

func init() {
    mysqlDB = sqlx.MustOpen("mysql", "test:test@tcp(localhost:3306)/test?parseTime=True&loc=Local&multiStatements=true&charset=utf8mb4")
}

// Tag 标签结构定义
type Tag struct {
    TagID int    `db:"id"`
    Name  string `db:"name"`
}

// NewTagReqBody 创建标签的请求体
type NewTagReqBody struct {
    Name string `json:"name"`
}

// OnNewTag 创建标签
func OnNewTag(c *gin.Context) {
    var reqBody NewTagReqBody
    if bindErr := c.BindJSON(&reqBody); bindErr != nil {
        c.JSON(http.StatusBadRequest, gin.H{
            "status":  http.StatusBadRequest,
            "message": bindErr.Error(),
        })
    }

    // 判断传入的 tag 名称是否为空
    tagName := strings.TrimSpace(reqBody.Name)
    if tagName == "" {
        c.JSON(http.StatusBadRequest, gin.H{
            "status":  http.StatusBadRequest,
            "message": "invalid name",
        })
        return
    }

    var queryTag Tag
    queryErr := mysqlDB.Get(&queryTag, "select id, name from tag_tbl where name = ?", tagName)
    if queryErr == nil {
        // tag 已经存在
        c.JSON(http.StatusOK, gin.H{
            "tag_id": queryTag.TagID,
        })
        return
    }

    // 查询 mysql 出现错误
    if queryErr != nil && queryErr != sql.ErrNoRows {
        c.JSON(http.StatusInternalServerError, gin.H{
            "status":  http.StatusInternalServerError,
            "message": queryErr.Error(),
        })
        return
    }

    // tag 不存在，创建 tag
    result, execErr := mysqlDB.Exec("insert into tag_tbl (name) values (?) on duplicate key update created_at = now()", tagName)
    if execErr != nil {
        c.JSON(http.StatusInternalServerError, gin.H{
            "status":  http.StatusInternalServerError,
            "message": execErr.Error(),
        })
        return
    }

    tagID, err := result.LastInsertId()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{
            "status":  http.StatusInternalServerError,
            "message": err.Error(),
        })
        return
    }

    c.JSON(http.StatusOK, gin.H{
        "tag_id": tagID,
    })
}

// OnSearchTag 搜索标签
func OnSearchTag(c *gin.Context) {
    c.JSON(http.StatusOK, gin.H{
        "matches": []struct{}{},
    })
}

func main() {
    r := gin.Default()

    r.POST("/api/tag", OnNewTag)
    r.POST("/api/tag/search", OnSearchTag)

    r.Run(":9800")
}
```

### 实现搜索标签的 API

导入 elasticsearch 包：

```go
import (
    ...
    elasticsearch7 "github.com/elastic/go-elasticsearch/v7"
)
```

声明 esClient 变量：

```go
var (
    mysqlDB  *sqlx.DB
    esClient *elasticsearch7.Client
)
```

在 init 函数中初始化 esClient：

```go
func init() {
	// 初始化 mysql
	mysqlDB = sqlx.MustOpen("mysql", "test:test@tcp(localhost:3306)/test?parseTime=True&loc=Local&multiStatements=true&charset=utf8mb4")

	// 初始化 ES
	esConf := elasticsearch7.Config{
		Addresses: []string{"http://localhost:9200"},
	}
	es, err := elasticsearch7.NewClient(esConf)
	if err != nil {
		panic(err)
	}

	res, err := es.Info()
	if err != nil {
		panic(err)
	}

	if res.IsError() {
		panic(res.String())
	}

	esClient = es
}
```

#### 把标签添加至 ES 索引

为了能在 ES 上搜到标签，我们需要在添加标签的时候，把标签添加至 ES 索引中。

先修改 Tag 结构，增加 JSON Tag, 并添加转换成 JSON 字符串的方法:

```go
// Tag 标签结构定义
type Tag struct {
	TagID int    `db:"id" json:"tag_id"`
	Name  string `db:"name" json:"name"`
}

// MustToJSON 将结构转换成 JSON
func (t *Tag) MustToJSON() string {
	bs, err := json.Marshal(t)
	if err != nil {
		panic(err)
	}
	return string(bs)
}
```

然后添加一个上报 Tag 到 ES 索引的函数:

```go
// ReportTagToES 上报 Tag 到 ES
func ReportTagToES(tag *Tag) {
	req := esapi.IndexRequest{
		Index:        "test",
		DocumentType: "tag",
		DocumentID:   strconv.Itoa(tag.TagID),
		Body:         strings.NewReader(tag.MustToJSON()),
		Refresh:      "true",
	}

	resp, err := req.Do(context.Background(), esClient)
	if err != nil {
		log.Printf("ESIndexRequestErr: %s", err.Error())
		return
	}

	defer resp.Body.Close()
	if resp.IsError() {
		log.Printf("ESIndexRequestErr: %s", resp.String())
	} else {
		log.Printf("ESIndexRequestOk: %s", resp.String())
	}
}
```

在 OnNewTag 函数的底部增加上报的逻辑：

```go
func OnNewTag(c *gin.Context) {

    ... 
    
	tagID, err := result.LastInsertId()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  http.StatusInternalServerError,
			"message": err.Error(),
		})
		return
	}
    
	// 添加到 ES 索引
	newTag := &Tag{TagID: int(tagID), Name: tagName}
	go ReportTagToES(newTag)

	c.JSON(http.StatusOK, gin.H{
		"tag_id": tagID,
	})
}
```

重新启动服务，然后测试创建 Tag，观察日志:

```
2020/06/05 11:29:11 ESIndexRequestOk: [201 Created] {"_index":"test","_type":"tag","_id":"4","_version":1,"result":"created","forced_refresh":true,"_shards":{"total":2,"successful":1,"failed":0},"_seq_no":3,"_primary_term":1}
```

再调用 ES 的 API 验证一下:

```
curl -XGET "localhost:9200/test/tag/4"

{"_index":"test","_type":"tag","_id":"4","_version":1,"_seq_no":3,"_primary_term":1,"found":true,"_source":{"tag_id":4,"name":"测试手段"}}
```

#### 完善搜索逻辑

新增一个 SearchTagReqBody 结构，作为搜索标签的请求体

```go
type SearchTagReqBody struct {
	Keyword string `json:"keyword"`
}
```

在 OnSearchTag 函数里面增加一些基本的参数校验：

```go
func OnSearchTag(c *gin.Context) {
	var reqBody SearchTagReqBody
	if bindErr := c.BindJSON(&reqBody); bindErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  http.StatusBadRequest,
			"message": bindErr.Error(),
		})
		return
	}

	searchKeyword := strings.TrimSpace(reqBody.Keyword)
	if searchKeyword == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  http.StatusBadRequest,
			"message": "invalid keyword",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"matches": []struct{}{},
	})
}
```

增加一个 `O` 结构作为 `map[string]interface{}` 的别名，并且为这个结构添加一个 `MustToJSONBytesBuffer() *bytes.Buffer` 的方法：

```go
type O map[string]interface{}

func (o *O) MustToJSONBytesBuffer() *bytes.Buffer {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(o); err != nil {
		panic(err)
	}

	return &buf
}
```

定义这个 `O` 是为了等会构建 ES 查询提供一点便利。

增加 SearchTagsFromES 函数，从 ES 上搜索 Tags：

```go
func SearchTagsFromES(keyword string) ([]*Tag, error) {
	// 构建查询
	query := O{
		"query": O{
			"match_phrase_prefix": O{
				"name":           keyword,
				"max_expansions": 50,
			},
		},
	}
	jsonBuf := query.MustToJSONBytesBuffer()

	// 发出查询请求
	resp, err := esClient.Search(
		esClient.Search.WithContext(context.Background()),
		esClient.Search.WithIndex("test"),
		esClient.Search.WithBody(jsonBuf),
	)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.IsError() {
		return nil, errors.New(resp.Status())
	}

	js, err := simplejson.NewFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	hitsJS := js.GetPath("hits", "hits")
	hits, err := hitsJS.Array()
	if err != nil {
		return nil, err
	}

	hitsLen := len(hits)
	if hitsLen == 0 {
		return []*Tag{}, nil
	}

	tags := make([]*Tag, 0, len(hits))
	for idx := 0; idx < hitsLen; idx++ {
		sourceJS := hitsJS.GetIndex(idx).Get("_source")

		tagID, err := sourceJS.Get("tag_id").Int()
		if err != nil {
			return nil, err
		}

		tagName, err := sourceJS.Get("name").String()
		if err != nil {
			return nil, err
		}

		tagEntity := &Tag{TagID: tagID, Name: tagName}
		tags = append(tags, tagEntity)
	}

	return tags, nil
}
``` 

修改 OnSearchTag 函数，加入搜索的逻辑：

```go
func OnSearchTag(c *gin.Context) {
	var reqBody SearchTagReqBody
	if bindErr := c.BindJSON(&reqBody); bindErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  http.StatusBadRequest,
			"message": bindErr.Error(),
		})
		return
	}

	searchKeyword := strings.TrimSpace(reqBody.Keyword)
	if searchKeyword == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  http.StatusBadRequest,
			"message": "invalid keyword",
		})
		return
	}

	tags, err := SearchTagsFromES(reqBody.Keyword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  http.StatusInternalServerError,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"matches": tags,
	})
}
```

重新启动服务，然后添加一个美食标签，然后再搜索：

```
curl --request GET \
  --url http://localhost:9800/api/tag/search \
  --header 'content-type: application/json' \
  --data '{
	"keyword": "美食"
}'

// response:

{
  "matches": [
    {
      "tag_id": 5,
      "name": "美食"
    }
  ]
}
```

#### 搜索 API 最终效果

先清空一下 MySQL 的历史数据，之前添加标签的时候，还没有添加到 ES 的索引里面：

```
truncate tag_tbl;
```

同时也清理一下 ES 索引：

```
curl -XDELETE "localhost:9200/test"
```

接下来添加一批 Tag:

```
美食
美食街
美食节
美食节趣闻
美食节三剑客
美食天堂
美食的诱惑
美食在中国
美食街都有啥
```

搜索 “美食”：

```json
{
  "matches": [
    {
      "tag_id": 1,
      "name": "美食"
    },
    {
      "tag_id": 2,
      "name": "美食街"
    },
    {
      "tag_id": 3,
      "name": "美食节"
    },
    {
      "tag_id": 6,
      "name": "美食天堂"
    },
    {
      "tag_id": 4,
      "name": "美食节趣闻"
    },
    {
      "tag_id": 7,
      "name": "美食的诱惑"
    },
    {
      "tag_id": 8,
      "name": "美食在中国"
    },
    {
      "tag_id": 5,
      "name": "美食节三剑客"
    },
    {
      "tag_id": 9,
      "name": "美食街都有啥"
    }
  ]
}
```

搜索 “美食街”：

```json
{
  "matches": [
    {
      "tag_id": 2,
      "name": "美食街"
    },
    {
      "tag_id": 9,
      "name": "美食街都有啥"
    }
  ]
}
```

搜索 “美食节”：

```json
{
  "matches": [
    {
      "tag_id": 3,
      "name": "美食节"
    },
    {
      "tag_id": 4,
      "name": "美食节趣闻"
    },
    {
      "tag_id": 5,
      "name": "美食节三剑客"
    }
  ]
}
```

### 实现关联标签到实体 API

定义实体关联 Tag 的结构：

```go
type EntityTag struct {
	LinkID   int `db:"id" json:"-"`
	EntityID int `db:"entity_id" json:"entity_id"`
	TagID    int `db:"tag_id" json:"tag_id"`
}
```

定义请求体:

```go
type LinkEntityReqBody struct {
	EntityID int `json:"entity_id"`
	TagID    int `json:"tag_id"`
}
```

开始编写 OnLinkEntity 里面的逻辑，首先先做基本的参数校验：

```go
var reqBody LinkEntityReqBody
if bindErr := c.BindJSON(&reqBody); bindErr != nil {
	c.JSON(http.StatusBadRequest, gin.H{
		"status":  http.StatusBadRequest,
		"message": bindErr.Error(),
	})
	return
}

if reqBody.EntityID == 0 || reqBody.TagID == 0 {
	c.JSON(http.StatusBadRequest, gin.H{
		"status":  http.StatusBadRequest,
		"message": "request params error",
	})
	return
}
```

查询是否标签已经关联过该实体，如果已经关联过，则直接返回：

```go
var entityTag EntityTag
queryErr := mysqlDB.Get(
	&entityTag,
	"select id, entity_id, tag_id from entity_tag_tbl where entity_id = ? and tag_id = ?",
	reqBody.EntityID, reqBody.TagID,
)

if queryErr == nil {
	// 已经存在关联
	c.JSON(http.StatusOK, gin.H{
		"link_id": entityTag.LinkID,
	})
	return
}

if queryErr != sql.ErrNoRows {
	// 查询错误
	c.JSON(http.StatusInternalServerError, gin.H{
		"status":  http.StatusInternalServerError,
		"message": queryErr.Error(),
	})
	return
}
```

判断一下 Tag 是否存在：

```go
var tag Tag
queryErr = mysqlDB.Get(
	&tag,
	"select id, name from tag_tbl where id = ?",
	reqBody.TagID,
)
if queryErr != nil {
	if queryErr != sql.ErrNoRows {
		// 查询错误
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  http.StatusInternalServerError,
			"message": queryErr.Error(),
		})
		return
	}

	// Tag 不存在
	c.JSON(http.StatusNotFound, gin.H{
		"status":  http.StatusNotFound,
		"message": "tag not found",
	})
	return
}
```

记录关联信息并返回关联 ID:

```go
execResult, execErr := mysqlDB.Exec(
	"insert into entity_tag_tbl (entity_id, tag_id) values (?, ?) on duplicate key update created_at = now()",
	reqBody.EntityID, reqBody.TagID,
)
if execErr != nil {
	// 插入失败
	c.JSON(http.StatusInternalServerError, gin.H{
		"status":  http.StatusInternalServerError,
		"message": execErr.Error(),
	})
	return
}

linkID, err := execResult.LastInsertId()
if err != nil {
	c.JSON(http.StatusInternalServerError, gin.H{
		"status":  http.StatusInternalServerError,
		"message": err.Error(),
	})
	return
}

c.JSON(http.StatusOK, gin.H{
	"link_id": int(linkID),
})
```

重启服务，创建一些关联：

```
curl --request POST \
  --url http://localhost:9800/api/tag/link_entity \
  --header 'content-type: application/json' \
  --data '{
	"entity_id": 1,
	"tag_id": 5
}'
```

可以通过数据库来验证一下：

```
mysql> select * from entity_tag_tbl;
+----+-----------+--------+---------------------+
| id | entity_id | tag_id | created_at          |
+----+-----------+--------+---------------------+
|  1 |         1 |      3 | 2020-06-05 15:03:00 |
|  2 |         1 |      1 | 2020-06-05 15:39:42 |
|  3 |         1 |      4 | 2020-06-05 15:39:47 |
|  4 |         1 |      2 | 2020-06-05 15:39:52 |
|  5 |         1 |      7 | 2020-06-05 15:55:59 |
|  6 |         1 |      5 | 2020-06-05 15:56:01 |
+----+-----------+--------+---------------------+
```

### 实现查询实体关联的标签列表 API

定义查询实体关联的标签列表的请求体:

```go
type EntityTagReqBody struct {
	EntityID int `json:"entity_id"`
}
```

编写 OnEntityTags 逻辑，和之前一样做参数校验：

```go
var reqBody EntityTagReqBody
if bindErr := c.BindJSON(&reqBody); bindErr != nil {
	c.JSON(http.StatusBadRequest, gin.H{
		"status":  http.StatusBadRequest,
		"message": bindErr.Error(),
	})
	return
}

if reqBody.EntityID == 0 {
	c.JSON(http.StatusBadRequest, gin.H{
		"status":  http.StatusBadRequest,
		"message": "request params error",
	})
	return
}
```

查询出实体关联的标签：

```go
entityTags := []*EntityTag{}
selectErr := mysqlDB.Select(&entityTags, "select id, entity_id, tag_id from entity_tag_tbl where entity_id = ? order by id", reqBody.EntityID)
if selectErr != nil {
	c.JSON(http.StatusInternalServerError, gin.H{
		"status":  http.StatusInternalServerError,
		"message": selectErr.Error(),
	})
	return
}

if len(entityTags) == 0 {
	c.JSON(http.StatusOK, gin.H{
		"tags": []*Tag{},
	})
	return
}
```

查询出标签列表，并返回：

```go
tagIDs := make([]int, 0, len(entityTags))
tagIndex := make(map[int]int, len(entityTags))
for index, entityTag := range entityTags {
	tagIndex[entityTag.TagID] = index
	tagIDs = append(tagIDs, entityTag.TagID)
}

queryTags, args, err := sqlx.In("select id, name from tag_tbl where id in (?)", tagIDs)
if err != nil {
	c.JSON(http.StatusInternalServerError, gin.H{
		"status":  http.StatusInternalServerError,
		"message": err.Error(),
	})
	return
}

tags := []*Tag{}
selectErr = mysqlDB.Select(&tags, queryTags, args...)
if selectErr != nil {
	c.JSON(http.StatusInternalServerError, gin.H{
		"status":  http.StatusInternalServerError,
		"message": selectErr.Error(),
	})
	return
}

sort.Slice(tags, func(i, j int) bool {
	return tagIndex[tags[i].TagID] < tagIndex[tags[j].TagID]
})

c.JSON(http.StatusOK, gin.H{
	"tags": tags,
})
```

重启服务测试一下：

```
curl --request GET \
  --url http://localhost:9800/api/tag/entity_tags \
  --header 'content-type: application/json' \
  --data '{
	"entity_id": 1
}'

// response

{
  "tags": [
    {
      "tag_id": 3,
      "name": "美食节"
    },
    {
      "tag_id": 1,
      "name": "美食"
    },
    {
      "tag_id": 4,
      "name": "美食节趣闻"
    },
    {
      "tag_id": 2,
      "name": "美食街"
    },
    {
      "tag_id": 7,
      "name": "美食的诱惑"
    },
    {
      "tag_id": 5,
      "name": "美食节三剑客"
    }
  ]
}
```