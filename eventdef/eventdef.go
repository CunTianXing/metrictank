/*
 * Copyright (c) 2015, Raintank Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package eventdef

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/codeskyblue/go-uuid"
	"github.com/ctdk/goas/v2/logger"
	elastigo "github.com/mattbaird/elastigo/lib"
	"github.com/raintank/raintank-metric/setting"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type EventDefinition struct {
	Id        string                 `json:"id"`
	EventType string                 `json:"event_type"`
	OrgId     int64                  `json:"org_id"`
	Severity  string                 `json:"severity"` // enum "INFO" "WARN" "ERROR" "OK"
	Source    string                 `json:"source"`
	Timestamp int64                  `json:"timestamp"`
	Message   string                 `json:"message"`
	Extra     map[string]interface{} `json:"-"`
}

type requiredField struct {
	StructName string
	Seen       bool
}

func (e *EventDefinition) UnmarshalJSON(raw []byte) error {
	s := reflect.TypeOf(*e)
	numFields := s.NumField()
	//lets start by unmashaling into a basic map datastructure
	event := make(map[string]interface{}, numFields)
	err := json.Unmarshal(raw, &event)
	if err != nil {
		return err
	}

	//lets get a list of our required fields.
	requiredFields := make(map[string]*requiredField, numFields - 2)

	for i := 0; i < numFields; i++ {
		field := s.Field(i)
		name := field.Name
		// look at the field Tags to work out the property named used in the
		// JSON document.
		tag := field.Tag.Get("json")
		if tag != "" && tag != "-" {
			name = tag
		}
		//all fields except 'Extra' and 'Id' are required.
		if name != "Extra" && name != "id" {
			requiredFields[name] = &requiredField{
				StructName: field.Name,
				Seen:       false,
			}
		}
	}

	e.Extra = make(map[string]interface{})
	for k, v := range event {
		def, ok := requiredFields[k]
		// anything that is not a required field gets
		// stored in our 'Extra' field.
		if !ok {
			e.Extra[k] = v
		} else {
			//coerce any float64 values to int64
			if reflect.ValueOf(v).Type().Name() == "float64" {
				v = int64(v.(float64))
			}
			value := reflect.ValueOf(v)
			if value.IsValid() {
				reflect.ValueOf(e).Elem().FieldByName(def.StructName).Set(value)
			} else {
				logger.Warningf("Yikes, in eventdef %s had the zero value! %v", k, v)
			}
			def.Seen = true
		}
	}

	//make sure all required fields were present.
	for _, v := range requiredFields {
		if !v.Seen {
			return fmt.Errorf("Required field '%s' missing", v.StructName)
		}
	}
	return nil
}

func (e *EventDefinition) MarshalJSON() ([]byte, error) {
	//convert our Event object to a map[string]
	value := reflect.ValueOf(*e)
	numFields := value.Type().NumField()
	event := make(map[string]interface{}, numFields + len(e.Extra))

	value := reflect.ValueOf(*e)
	for i := 0; i < numFields; i++ {
		field := value.Type().Field(i)
		name := field.Name
		tag := field.Tag.Get("json")
		if tag != "" && tag != "-" {
			name = tag
		}
		if name == "Extra" {
			//anything that was in Extra[] becomes a toplevel property again.
			for k, v := range e.Extra {
				event[k] = v
			}
		} else {
			v, err := encode(value.FieldByName(field.Name))
			if err != nil {
				return nil, err
			}
			event[name] = v
		}
	}
	//Marshal our map[string] into a JSON string (byte[]).
	raw, err := json.Marshal(&event)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// convert reflect.Value object to interface{}
func encode(v reflect.Value) (interface{}, error) {
	switch v.Type().Kind() {
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.String:
		return v.String(), nil
	case reflect.Int64:
		return v.Int(), nil
	default:
		return nil, errors.New("Unsupported type")
	}
}

var es *elastigo.Conn

func InitElasticsearch() error {
	es = elastigo.NewConn()
	es.Domain = setting.Config.ElasticsearchDomain // needs to be configurable obviously
	es.Port = strconv.Itoa(setting.Config.ElasticsearchPort)
	if setting.Config.ElasticsearchUser != "" && setting.Config.ElasticsearchPasswd != "" {
		es.Username = setting.Config.ElasticsearchUser
		es.Password = setting.Config.ElasticsearchPasswd
	}

	return nil
}

func (e *EventDefinition) Save() error {
	if e.Id == "" {
		u := uuid.NewRandom()
		e.Id = u.String()
	}
	if e.Timestamp == 0 {
		// looks like this expects timestamps in milliseconds
		e.Timestamp = time.Now().UnixNano() / int64(time.Millisecond)
	}
	if err := e.validate(); err != nil {
		return err
	}
	resp, err := es.Index("events", e.EventType, e.Id, nil, e)
	logger.Debugf("response ok? %v", resp.Ok)
	if err != nil {
		return err
	}

	return nil
}

func (e *EventDefinition) validate() error {
	if e.EventType == "" || e.OrgId == 0 || e.Source == "" || e.Timestamp == 0 || e.Message == "" {
		err := fmt.Errorf("event definition not valid")
		return err
	}
	switch strings.ToLower(e.Severity) {
	case "info", "ok", "warn", "error", "warning", "critical":
		// nop
	default:
		err := fmt.Errorf("'%s' is not a valid severity level", e.Severity)
		return err
	}
	return nil
}

func EventFromJSON(b []byte) (*EventDefinition, error) {
	e := new(EventDefinition)
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	return e, nil
}
