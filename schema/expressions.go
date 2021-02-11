package schema

import "github.com/zclconf/go-cty/cty"

type ExprConstraints []ExprConstraint

func (ec ExprConstraints) LiteralValueTypes() []cty.Type {
	types := make([]cty.Type, 0)

	for _, constraint := range ec {
		if lv, ok := constraint.(LiteralTypeExpr); ok {
			types = append(types, lv.Type)
		}
	}

	return types
}

type exprConsSigil struct{}

type ExprConstraint interface {
	isExprConstraintImpl() exprConsSigil
}

type LiteralTypeExpr struct {
	Type cty.Type
}

func (LiteralTypeExpr) isExprConstraintImpl() exprConsSigil {
	return exprConsSigil{}
}

func LiteralTypeOnly(t cty.Type) ExprConstraints {
	return ExprConstraints{
		LiteralTypeExpr{Type: t},
	}
}
