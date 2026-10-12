// Copyright 2025 V Kontakte LLC
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package pure

import (
	"github.com/VKCOM/tl/internal/tlast"
)

func (k *Kernel) convertTemplateArguments(args []tlast.TemplateArgument) []tlast.TL2TypeTemplate {
	var result []tlast.TL2TypeTemplate
	for _, arg := range args {
		result = append(result, tlast.TL2TypeTemplate{
			Name:       arg.FieldName,
			Category:   tlast.TL2TypeCategory{IsNatValue: arg.IsNat},
			PR:         arg.PR,
			PRName:     arg.PR,
			PRCategory: arg.PR,
		})
	}
	return result
}

func (k *Kernel) convertTypeArgument(tra tlast.ArithmeticOrType) tlast.TL2TypeArgument {
	if tra.IsArith {
		return tlast.TL2TypeArgument{
			Number:   tra.Arith.Res,
			IsNumber: true,
			PR:       tra.T.PR,
		}
	}
	return tlast.TL2TypeArgument{
		Type:   k.convertTypeRef(tra.T),
		Number: tra.Arith.Res,
		PR:     tra.T.PR,
	}
}

func (k *Kernel) convertTypeRef(tr tlast.TypeRef) tlast.TL2TypeRef {
	return tlast.TL1TypeRef(tr)
}
