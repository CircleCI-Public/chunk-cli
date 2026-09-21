package mutate

import "go/token"

// Gremlins operator names.
const (
	OpConditionalsBoundary     = "CONDITIONALS_BOUNDARY"
	OpConditionalsNegation     = "CONDITIONALS_NEGATION"
	OpArithmeticBase           = "ARITHMETIC_BASE"
	OpIncrementDecrement       = "INCREMENT_DECREMENT"
	OpInvertAssignments        = "INVERT_ASSIGNMENTS"
	OpInvertBitwise            = "INVERT_BITWISE"
	OpInvertBitwiseAssignments = "INVERT_BITWISE_ASSIGNMENTS"
)

// operatorMutations maps each gremlins operator name to its token replacements.
var operatorMutations = map[string]map[token.Token]token.Token{
	OpConditionalsBoundary: {
		token.LSS: token.LEQ,
		token.LEQ: token.LSS,
		token.GTR: token.GEQ,
		token.GEQ: token.GTR,
	},
	OpConditionalsNegation: {
		token.EQL:  token.NEQ,
		token.NEQ:  token.EQL,
		token.LSS:  token.GEQ,
		token.GEQ:  token.LSS,
		token.GTR:  token.LEQ,
		token.LEQ:  token.GTR,
		token.LAND: token.LOR,
		token.LOR:  token.LAND,
	},
	OpArithmeticBase: {
		token.ADD: token.SUB,
		token.SUB: token.ADD,
		token.MUL: token.QUO,
		token.QUO: token.MUL,
		token.REM: token.MUL,
	},
	OpIncrementDecrement: {
		token.INC: token.DEC,
		token.DEC: token.INC,
	},
	OpInvertAssignments: {
		token.ADD_ASSIGN: token.SUB_ASSIGN,
		token.SUB_ASSIGN: token.ADD_ASSIGN,
		token.MUL_ASSIGN: token.QUO_ASSIGN,
		token.QUO_ASSIGN: token.MUL_ASSIGN,
		token.REM_ASSIGN: token.MUL_ASSIGN,
	},
	OpInvertBitwise: {
		token.AND: token.OR,
		token.OR:  token.AND,
		token.XOR: token.AND,
	},
	OpInvertBitwiseAssignments: {
		token.AND_ASSIGN: token.OR_ASSIGN,
		token.OR_ASSIGN:  token.AND_ASSIGN,
		token.XOR_ASSIGN: token.AND_ASSIGN,
	},
}

// mutatedToken returns the replacement token for the given operator and original token.
func mutatedToken(operator string, tok token.Token) (token.Token, bool) {
	replacements, ok := operatorMutations[operator]
	if !ok {
		return tok, false
	}
	rep, ok := replacements[tok]
	return rep, ok
}
