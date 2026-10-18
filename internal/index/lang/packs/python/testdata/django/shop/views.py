from django.shortcuts import render

from .models import Product


def index(request):
    return render(request, "index.html", {"products": Product.objects.all()})


def detail(request, pk):
    ...
