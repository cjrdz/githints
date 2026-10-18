from rest_framework import viewsets
from rest_framework.views import APIView

from .models import Product
from .serializers import ProductSerializer


class ProductViewSet(viewsets.ModelViewSet):
    queryset = Product.objects.all()
    serializer_class = ProductSerializer


class HealthView(APIView):
    pass
