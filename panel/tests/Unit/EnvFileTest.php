<?php

namespace Tests\Unit;

use App\Support\EnvFile;
use Dotenv\Dotenv;
use InvalidArgumentException;
use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\TestCase;

/** The .env editing the credentials writer relies on: every other line stays as it was. */
final class EnvFileTest extends TestCase
{
    public function testEnvFilePreservesOtherLines(): void
    {
        $text = "# Application\nAPP_NAME=Shop\n\nAPP_KEY=base64:abc=\nexport KEEP=1\nnot a pair\nDB_HOST=localhost\n";

        $env = EnvFile::parse($text)->set('DB_HOST', '127.0.0.1')->set('DB_PORT', '3306');

        $this->assertSame("# Application\nAPP_NAME=Shop\n\nAPP_KEY=base64:abc=\nexport KEEP=1\nnot a pair\nDB_HOST=127.0.0.1\nDB_PORT=3306\n", $env->toString());
    }

    public function testRoundTripWithoutChangesIsIdentical(): void
    {
        foreach (['', 'A=1', "A=1\n", "A=1\r\nB=2\r\n", "\n\nA=1\n\n", '# only a comment'] as $text) {
            $this->assertSame($text, EnvFile::parse($text)->toString());
        }
    }

    public function testMissingKeyIsAppendedAfterAFinalLineWithoutNewline(): void
    {
        $this->assertSame("A=1\nB=2\n", EnvFile::parse('A=1')->set('B', '2')->toString());
        $this->assertSame("B=2\n", EnvFile::parse('')->set('B', '2')->toString());
    }

    /** A key that appears twice is updated where it first appears, and the later copies are removed. */
    public function testDuplicateKeyIsUpdatedInPlaceAndTheRestRemoved(): void
    {
        $env = EnvFile::parse("A=1\nDB_HOST=one\nB=2\nDB_HOST=two\nC=3\n")->set('DB_HOST', '127.0.0.1');

        $this->assertSame("A=1\nDB_HOST=127.0.0.1\nB=2\nC=3\n", $env->toString());
    }

    public function testKeyWithSpacesAroundEqualsIsFound(): void
    {
        $this->assertSame("DB_HOST=x\n", EnvFile::parse("DB_HOST = old\n")->set('DB_HOST', 'x')->toString());
    }

    public function testCommentedKeyIsNotTheKey(): void
    {
        $this->assertSame("# DB_HOST=old\nDB_HOST=x\n", EnvFile::parse("# DB_HOST=old\n")->set('DB_HOST', 'x')->toString());
    }

    public function testKeepsCrLfLineEndings(): void
    {
        $this->assertSame("A=1\r\nB=2\r\n", EnvFile::parse("A=1\r\n")->set('B', '2')->toString());
    }

    /** @return array<string, array{string, string}> */
    public static function specialValues(): array
    {
        return [
            'plain' => ['abc-DEF_123', 'abc-DEF_123'],
            'empty' => ['', ''],
            'space' => ['a b', '"a b"'],
            'hash' => ['a#b', '"a#b"'],
            'double quote' => ['a"b', '"a\"b"'],
            'single quote' => ["a'b", '"a\'b"'],
            'dollar' => ['pa$word', '"pa\$word"'],
            'equals' => ['a=b', '"a=b"'],
            'trailing space' => ['ab ', '"ab "'],
            'backslash' => ['a\\b', '"a\\\\b"'],
            'both' => ['a\\"b', '"a\\\\\\"b"'],
        ];
    }

    #[DataProvider('specialValues')]
    public function testEnvFileQuotesSpecialValues(string $value, string $written): void
    {
        $this->assertSame("KEY={$written}\n", EnvFile::parse('')->set('KEY', $value)->toString());
        // What was written reads back as the value, as Laravel reads it.
        $this->assertSame(['KEY' => $value], Dotenv::parse("KEY={$written}\n"));
    }

    /** phpdotenv replaces ${VAR} in an unescaped double-quoted value; the escaped dollar sign stays a literal one. */
    public function testDollarSignIsNotInterpolated(): void
    {
        $text = EnvFile::parse("HOME_DIR=/home/s_shop\n")->set('PASS', 'a${HOME_DIR}b$HOME_DIR')->toString();

        $this->assertSame("HOME_DIR=/home/s_shop\nPASS=\"a\\\${HOME_DIR}b\\\$HOME_DIR\"\n", $text);
        $this->assertSame('a${HOME_DIR}b$HOME_DIR', Dotenv::parse($text)['PASS']);
    }

    /** @return array<string, array{string}> */
    public static function lineBreaks(): array
    {
        return ['line feed' => ["a\nb"], 'carriage return' => ["a\rb"], 'nul' => ["a\0b"]];
    }

    #[DataProvider('lineBreaks')]
    public function testSetRefusesLineBreaksAndNul(string $value): void
    {
        $env = EnvFile::parse("A=1\n");

        try {
            $env->set('B', $value);
            $this->fail('a value with a line break or NUL was accepted');
        } catch (InvalidArgumentException) {
        }
        $this->assertSame("A=1\n", $env->toString());
    }

    /** An existing multi-line quoted value stays as it is, and its inner lines are not keys. */
    public function testMultiLineQuotedValueIsKeptAndNotSearched(): void
    {
        $text = "A=1\nCERT=\"-----BEGIN-----\nDB_HOST=inside\nDB_PORT=inside\n-----END-----\"\nDB_HOST=old\n";

        $env = EnvFile::parse($text);

        $this->assertSame($text, $env->toString());
        $this->assertSame(
            "A=1\nCERT=\"-----BEGIN-----\nDB_HOST=inside\nDB_PORT=inside\n-----END-----\"\nDB_HOST=new\nDB_USERNAME=u\n",
            $env->set('DB_HOST', 'new')->set('DB_USERNAME', 'u')->toString(),
        );
    }

    /** Setting the key of a multi-line value replaces all of its lines. */
    public function testSettingAMultiLineKeyReplacesItsWholeValue(): void
    {
        $text = "A=1\nCERT=\"line one\nline two\"\nB=2\n";

        $this->assertSame("A=1\nCERT=x\nB=2\n", EnvFile::parse($text)->set('CERT', 'x')->toString());
    }

    public function testClosedQuotesAndEscapedQuotesDoNotOpenAMultiLineValue(): void
    {
        $text = "A=\"one line\"\nB=\"say \\\"hi\\\"\"\nC=3\n";

        $this->assertSame("A=\"one line\"\nB=\"say \\\"hi\\\"\"\nC=4\n", EnvFile::parse($text)->set('C', '4')->toString());
    }
}
