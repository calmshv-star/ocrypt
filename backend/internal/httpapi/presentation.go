package httpapi

import (
	"github.com/calmshv-star/ocrypt/backend/internal/chains"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

func presentPaymentRoute(route domain.PaymentRoute) domain.PaymentRoute {
	route.Address = chains.DisplayAddress(route.ChainID, route.Address)
	return route
}

func presentPaymentIntent(intent domain.PaymentIntent) domain.PaymentIntent {
	intent.Routes = append([]domain.PaymentRoute(nil), intent.Routes...)
	for index := range intent.Routes {
		intent.Routes[index] = presentPaymentRoute(intent.Routes[index])
	}
	return intent
}
