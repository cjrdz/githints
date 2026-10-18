package com.example.shop.service;

import com.example.shop.domain.Product;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;

@Service
public class PricingService {
    @Scheduled(cron = "0 0 * * * *")
    public void refreshPrices() {}
}
