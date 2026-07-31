package schema

import (
	"errors"
	"fmt"
)

type Node interface {
	build() (map[string]any, error)
}

/* used for objectnode.*/
type Field struct {
	Name     string
	Required bool
	Schema   Node
}

/*
 * the introduction sheet of tool,only
 * struct opened to other package.
 */
type Spec struct {
	Name        string
	Description string
	Param       Node
}

func NewSpec(name, description string, param Node) Spec {
	return Spec{
		Name:        name,
		Description: description,
		Param:       param,
	}
}

/* basic data struct for certain node.*/
type stringNode struct {
	decription string
	enum       []string
}

type arrayNode struct {
	description string
	item        Node
}

type integarNode struct {
	description string
}

type booleanNode struct {
	description string
}

/* objectNode is used for spec subNode*/
type objectNode struct {
	fields                 []Field
	additionalBoolProperty bool
}

/* normal method for normal data-struct node*/
func String(description string) Node { return stringNode{decription: description} }

func EnumString(description string, values ...string) Node {
	return stringNode{
		decription: description,
		enum:       append([]string(nil), values...),
	}
}

func Array(description string, item Node) Node {
	return arrayNode{
		description: description,
		item:        item,
	}
}

func Integar(description string) Node {
	return integarNode{
		description: description,
	}
}

func Boolean(description string) Node {
	return booleanNode{
		description: description,
	}
}

/* object initialize for object Node.*/
func Object(fields ...Field) Node {
	return objectNode{
		fields:                 append([]Field(nil), fields...),
		additionalBoolProperty: false,
	}
}

/* certain initialize function for field*/
func Required(name string, schema Node) Field {
	return Field{
		Name:     name,
		Required: true,
		Schema:   schema,
	}
}

func Optional(name string, schema Node) Field {
	return Field{
		Name:     name,
		Required: false,
		Schema:   schema,
	}
}

/* certian function convert sepc parameter into map format.*/
func (s Spec) Parameter() (map[string]any, error) {
	if s.Name == "" {
		return nil, errors.New("tool name is empty")
	}
	if s.Description == "" {
		return nil, fmt.Errorf("tool %q has nil params", s.Name)
	}
	result, err := s.Param.build()
	if err != nil {
		return nil, fmt.Errorf("tool %q parameters: %w", s.Name, err)
	}
	return result, err
}
