<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/** GitHub delivery ids already handled, for 24 hours, so a redelivery deploys once (R02-D11). */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('webhook_deliveries', function (Blueprint $table) {
            $table->id();
            $table->string('delivery_id')->unique();
            $table->foreignId('site_id')->constrained()->cascadeOnDelete();
            $table->timestamp('received_at')->index();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('webhook_deliveries');
    }
};
