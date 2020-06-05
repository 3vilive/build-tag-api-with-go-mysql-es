package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/bitly/go-simplejson"
	elasticsearch7 "github.com/elastic/go-elasticsearch/v7"
	esapi "github.com/elastic/go-elasticsearch/v7/esapi"
	"github.com/gin-gonic/gin"

	"github.com/jmoiron/sqlx"

	_ "github.com/go-sql-driver/mysql" // mysql driver
)

var (
	mysqlDB  *sqlx.DB
	esClient *elasticsearch7.Client
)

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

// O is shortcut of map[string]interface{}
type O map[string]interface{}

// MustToJSONBytesBuffer 将 O 结构转换成 JSON 并返回对应的 Buffer
func (o *O) MustToJSONBytesBuffer() *bytes.Buffer {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(o); err != nil {
		panic(err)
	}

	return &buf
}

// SearchTagsFromES 从 ES 搜索标签
func SearchTagsFromES(keyword string) ([]*Tag, error) {
	// 构建查询
	query := O{
		"query": O{
			"match_phrase_prefix": O{
				"name": keyword,
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
	if queryErr != sql.ErrNoRows {
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

	// 添加到 ES 索引
	newTag := &Tag{TagID: int(tagID), Name: tagName}
	go ReportTagToES(newTag)

	c.JSON(http.StatusOK, gin.H{
		"tag_id": tagID,
	})
}

// SearchTagReqBody 搜索标签的请求体
type SearchTagReqBody struct {
	Keyword string `json:"keyword"`
}

// OnSearchTag 搜索标签
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
		log.Printf("SearchTagsFromESErr: %s", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  http.StatusInternalServerError,
			"message": fmt.Errorf("SearchTagsFromESErr: %s", err).Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"matches": tags,
	})
}

// EntityTag 实体关联的 Tag
type EntityTag struct {
	LinkID   int `db:"id" json:"-"`
	EntityID int `db:"entity_id" json:"entity_id"`
	TagID    int `db:"tag_id" json:"tag_id"`
}

// LinkEntityReqBody 关联标签到实体请求体
type LinkEntityReqBody struct {
	EntityID int `json:"entity_id"`
	TagID    int `json:"tag_id"`
}

// OnLinkEntity 关联标签到实体请求体
func OnLinkEntity(c *gin.Context) {
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

	// 查询是否已经关联过
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

	// 查询 Tag 信息
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

	// 插入关联记录
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
}

// EntityTagReqBody 查询实体关联的标签列表的请求体
type EntityTagReqBody struct {
	EntityID int `json:"entity_id"`
}

// OnEntityTags 查询实体关联的标签列表
func OnEntityTags(c *gin.Context) {
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
}

func main() {
	r := gin.Default()

	r.POST("/api/tag", OnNewTag)
	r.GET("/api/tag/search", OnSearchTag)
	r.POST("/api/tag/link_entity", OnLinkEntity)
	r.GET("/api/tag/entity_tags", OnEntityTags)

	r.Run(":9800")
}
